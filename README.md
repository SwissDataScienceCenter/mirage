# Mirage

Mirage makes a Kubernetes controller believe it has cluster-wide access when it only has access to
one namespace.

Plenty of operators are written and packaged on the assumption that they own the cluster: their
charts create `ClusterRole`s, and their code lists and watches resources across all namespaces with
no option to narrow the scope. If you only have permissions in a single namespace — a shared,
multi-tenant cluster, say — those operators simply will not start.

Mirage runs as a sidecar next to such a controller and rewrites its API requests. A cluster-wide
`LIST` of Builds becomes a `LIST` of Builds in one namespace. The controller cannot tell the
difference, and nothing about the cluster has to change.

> **Status: design only.** The decisions are recorded in [`docs/adr/`](./docs/adr/) and the domain
> language in [`CONTEXT.md`](./CONTEXT.md). No implementation exists yet.

## How it works

The controller — the **Client** — is pointed at Mirage with a mounted kubeconfig instead of the
real API server. Mirage inspects each request and does one of three things:

| Decision | Behaviour |
| --- | --- |
| **Rewrite** | The path is a collection path for a configured resource and names no namespace, so Mirage inserts the Target Namespace and forwards it. |
| **Mask** | The resource is configured as masked. Mirage answers itself, always reporting it as existing and empty, and never contacts the API server. |
| **Pass Through** | Everything else is forwarded byte-for-byte, unchanged. |

Two properties are worth knowing before you rely on it:

**Mirage never adds authority.** It forwards the Client's own `Authorization` header untouched and
holds no credentials of its own. The API server remains the sole enforcement point, so the worst a
broken Mirage can do is cause the Client to receive `403`s — it can never grant access the
Client's ServiceAccount did not already have.

**Mirage only rewrites what you list.** Anything not named in the config is passed through
unchanged. A forgotten entry therefore fails exactly as it would have without Mirage — a `403` from
the API server — rather than silently exposing another tenant's namespace.

## Configuration

```yaml
handled:
  - {group: "",            resource: pods}          # "" is the core group
  - {group: tekton.dev,    resource: taskruns}
  - {group: shipwright.io, resource: builds}
  - {group: shipwright.io, resource: buildruns}
  - {group: shipwright.io, resource: buildstrategies}

masked:
  - {group: shipwright.io, resource: clusterbuildstrategies, kind: ClusterBuildStrategy}
```

`resource` is the plural name as it appears in the URL. API versions are deliberately not part of
the config — an entry applies to every version of that resource, so a controller reading both
`v1alpha1` and `v1beta1` needs one entry, not two. `kind` is required for masked entries only, so
Mirage can name the empty list it synthesises (`ClusterBuildStrategyList`).

The Target Namespace is always the namespace Mirage's own Pod runs in, read from the projected
ServiceAccount volume. The Upstream API server is derived from Mirage's own
`KUBERNETES_SERVICE_HOST` / `KUBERNETES_SERVICE_PORT`. Neither is configurable.

## Installing

Mirage ships as a container image plus the patch below. There is no Helm chart and no operator.

**Requirements**

- Kubernetes 1.29+ / OpenShift 4.16+, for native sidecar support.
- The Client must honour `KUBECONFIG` — true of anything using controller-runtime's
  `GetConfig()`. A Client calling `rest.InClusterConfig()` directly will ignore it.
- The Client must **not** be installed by OLM, which reverts sidecar patches. See
  [ADR 0005](./docs/adr/0005-distribution-is-an-image-plus-a-kustomize-patch.md).

**1. Create the ConfigMap** holding Mirage's config and the kubeconfig the Client will use:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: mirage-config
data:
  config.yaml: |
    handled:
      - {group: shipwright.io, resource: builds}
      # ... as above
  kubeconfig: |
    apiVersion: v1
    kind: Config
    current-context: mirage
    clusters:
      - name: mirage
        cluster:
          server: http://127.0.0.1:8001
    users:
      - name: mirage
        user:
          tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
    contexts:
      - name: mirage
        context:
          cluster: mirage
          user: mirage
```

**2. Patch the Client's Deployment:**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: shipwright-build-controller
spec:
  template:
    spec:
      volumes:
        - name: mirage-config
          configMap:
            name: mirage-config
      initContainers:
        - name: mirage
          image: ghcr.io/OWNER/mirage:TAG
          restartPolicy: Always          # makes this a native sidecar
          args: ["--config", "/etc/mirage/config.yaml"]
          volumeMounts:
            - name: mirage-config
              mountPath: /etc/mirage
          startupProbe:
            httpGet: {path: /healthz, port: 8001}
          securityContext:
            runAsNonRoot: true
            allowPrivilegeEscalation: false
            capabilities: {drop: [ALL]}
            seccompProfile: {type: RuntimeDefault}
      containers:
        - name: shipwright-build-controller
          env:
            - name: KUBECONFIG
              value: /etc/mirage/kubeconfig
          volumeMounts:
            - name: mirage-config
              mountPath: /etc/mirage
```

`restartPolicy: Always` on an init container is what makes it a native sidecar: the kubelet starts
Mirage and waits for its `startupProbe` before the Client starts, and shuts it down only after the
Client has exited. Both matter — without the first the Client crash-loops on startup, and without
the second the Client's final API calls fail, including releasing its leader-election Lease.

**Do not remove or remap the default ServiceAccount mount.** It is load-bearing in three
independent ways: the kubeconfig reads the token from it, Mirage reads `ca.crt` from it to reach
the API server, and controller-runtime's leader election finds its namespace by reading the
`namespace` file directly. Nothing warns you if it disappears.

## Limitations

- **One namespace only.** Mirage cannot present a set of namespaces as the cluster; doing so would
  require merging list and watch streams, which is a different program. See
  [ADR 0003](./docs/adr/0003-one-namespace-and-no-body-rewriting.md).
- **Masking works for custom resources only.** Mirage answers masked requests in JSON and returns
  `406` for protobuf, mirroring how the API server treats CRDs. Built-in types, where client-go
  negotiates protobuf and will not fall back, would need protobuf support first.
- **Cluster-scoped resources you actually need cannot be faked.** Masking tells the Client the
  resource is empty. For Shipwright specifically this means every Build in the namespace must use
  `kind: BuildStrategy`; a Build referencing a `ClusterBuildStrategy` will never resolve its
  strategy. Most Shipwright examples use `ClusterBuildStrategy`, so this is the first mistake
  people make.

## Debugging

Mirage logs its full resolved configuration at startup — Target Namespace, Upstream, and every
handled and masked entry — so the first ten lines of its logs tell you what it actually loaded.

Run with debug logging for one structured line per request showing the decision, the inbound path
and the outbound path.

If the Client is getting unexplained `403`s, look for Mirage's warnings about cluster-wide requests
that were passed through: that is the signature of a resource missing from `handled`.
