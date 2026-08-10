# TODO

Open items from the design session. The design itself is settled — see
[`CONTEXT.md`](./CONTEXT.md) and [`docs/adr/`](./docs/adr/).

## Open questions

- [ ] **Can CI fetch the `envtest` binaries?** The integration tier of the test strategy runs a real
      `etcd` + `kube-apiserver` via `setup-envtest`. If CI is airgapped or cannot download them,
      that tier needs rethinking — and it is the tier guarding the streaming dependency in
      [ADR 0006](./docs/adr/0006-echo-proxy-middleware-and-the-streaming-dependency.md).

## Verify in code

- [ ] **Confirm `*echo.Response` implements `http.Flusher` in Echo v5.** `httputil.ReverseProxy`
      streams a WATCH only if the `ResponseWriter` it receives is flushable. This held in v4; v5
      changed `echo.Context` from an interface to a struct, so it is worth checking rather than
      assuming. If it ever stops being true the failure is silent — informers receive nothing and
      the controller simply never reconciles.

- [ ] **Drop the `events` entry from the starting config unless something needs it.** It was
      included speculatively. Shipwright writes events through a recorder, which is a namespaced
      POST that passes through fine, and no cluster-wide event LIST was found in its source. A
      spurious `confined` entry is harmless but misleading about what the Client actually does.

## Implementation

Nothing is built yet. In rough dependency order:

- [ ] `decide(method, path) → PassThrough | Confine | Mask` as a pure function, with table-driven
      tests covering the boundaries: insert-if-absent, explicit foreign namespace left untouched,
      unconfigured resources passed through, single-object and subresource paths untouched.
- [ ] Config loading and the startup log line that echoes the resolved config back.
- [ ] Echo v5 server: `/healthz` route, catch-all `/*` with the decide middleware and
      `middleware.Proxy` against a single-target balancer.
- [ ] Upstream transport: cluster CA from the projected ServiceAccount volume, host from Mirage's
      own `KUBERNETES_SERVICE_HOST` / `KUBERNETES_SERVICE_PORT`, no credentials of its own.
- [ ] Masked Resource handler: empty list at `resourceVersion: 1`, WATCH honouring `timeoutSeconds`
      and `allowWatchBookmarks`, `404` on named GET, `403` on mutation, `406` on protobuf, all
      errors as well-formed `metav1.Status`.
- [ ] The `Warn` heuristic: namespace-less collection request for an unconfigured resource.
- [ ] End-to-end test running a real client-go informer through Mirage — required, not optional.
- [ ] Container image: static binary, runs as an arbitrary UID, listens above port 1024.

## Deferred by decision

Not bugs, not oversights — each was considered and postponed. Revisit only if something real
demands it.

- [ ] Target Namespace as a config override, for the case where the Client's ServiceAccount has a
      RoleBinding in some other namespace.
- [ ] Self-signed TLS mode, for Clients that call `rest.InClusterConfig()` directly and therefore
      ignore `KUBECONFIG`. Needs cert generation plus a projected volume overriding `ca.crt`.
      See [ADR 0002](./docs/adr/0002-plaintext-loopback-not-tls.md).
- [ ] Masking `SelfSubjectAccessReview` to always answer `allowed: true`, for Clients that check
      their own permissions at startup and refuse to run. Shipwright does not, so this is unbuilt.
      It would not violate [ADR 0001](./docs/adr/0001-mirage-never-adds-authority.md) — the API
      server still enforces the real request — but it should be opt-in per config, never always-on.
