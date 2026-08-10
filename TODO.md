# TODO

Open items from the design session. The design itself is settled — see
[`CONTEXT.md`](./CONTEXT.md) and [`docs/adr/`](./docs/adr/).

## Open questions

- [ ] **Does envtest pass `--token-auth-file` through to the API server cleanly?** The integration
      suite depends on it — see the wiring problem below. First thing to check before building the
      suite out; the fallback is an mTLS transport, which costs the header-forwarding coverage.

- [ ] **Build tag plus its own CI job, or folded into `just test` so it always runs?** Affects how
      loudly a missing binary fails and how long the default test recipe takes.

**Resolved: CI can fetch the envtest binaries.** `ubuntu-latest`, no privileges, no Docker — just
network access to the `controller-tools` GitHub releases index, plus an `actions/cache` step keyed on
the pinned Kubernetes version. The old GCS bucket is gone; `setup-envtest` reads the releases index
now.

## Verify in code

- [x] **Confirm `*echo.Response` implements `http.Flusher` in Echo v5.** It does, and
      `internal/server/streaming_test.go` now pins it rather than leaving it to inspection. If it
      ever stops being true the failure is silent — informers receive nothing and the controller
      simply never reconciles — which is why the assertion is a test and not a comment. `mask` reaches
      the flusher through an `http.ResponseController` for the same reason.

- [ ] **Drop the `events` entry from the starting config unless something needs it.** It was
      included speculatively. Shipwright writes events through a recorder, which is a namespaced
      POST that passes through fine, and no cluster-wide event LIST was found in its source. A
      spurious `confined` entry is harmless but misleading about what the Client actually does.

## Implementation

Everything below the unit tier is built and covered by Ginkgo suites. What remains is the
integration tier, planned in the next section.

- [x] `decide(method, path) → PassThrough | Confine | Mask` as a pure function, with table-driven
      tests covering the boundaries: insert-if-absent, explicit foreign namespace left untouched,
      unconfigured resources passed through, single-object and subresource paths untouched.
- [x] Config loading and the startup log line that echoes the resolved config back.
- [x] Echo v5 server: `/healthz` route, catch-all `/*` with the decide middleware and
      `middleware.Proxy` against a single-target balancer.
- [x] Upstream transport: cluster CA from the projected ServiceAccount volume, host from Mirage's
      own `KUBERNETES_SERVICE_HOST` / `KUBERNETES_SERVICE_PORT`, no credentials of its own.
- [x] Masked Resource handler: empty list at `resourceVersion: 1`, WATCH honouring `timeoutSeconds`
      and `allowWatchBookmarks`, `404` on named GET, `403` on mutation, `406` on protobuf, all
      errors as well-formed `metav1.Status`.
- [x] The `Warn` heuristic: namespace-less collection request for an unconfigured resource.
- [x] Container image: static binary, runs as an arbitrary UID, listens above port 1024.
- [ ] End-to-end test running a real client-go informer through Mirage — required, not optional. See
      the integration plan below.

## Integration tests with envtest

`envtest` starts real `etcd` and `kube-apiserver` binaries as child processes on loopback and hands
back a `*rest.Config`. Not a fake and not a container — which is the point, since Mirage's whole job
is URL manipulation against a real API server's routing, and a fake would only re-assert whatever we
already believe about it.

**What it needs to run**

- [ ] The binaries: `etcd`, `kube-apiserver`, `kubectl`, ~150–200 MB, per platform. Fetched with
      `setup-envtest`, located at runtime via `KUBEBUILDER_ASSETS` or `BinaryAssetsDirectory`.
      Add `tool sigs.k8s.io/controller-runtime/tools/setup-envtest` to `go.mod` so `go tool
      setup-envtest` works with nothing installed by hand, matching how `ginkgo` is already wired.
- [ ] Pin the Kubernetes version deliberately — the README claims 1.29+, so the version the suite
      runs against is a choice to record, not a default to inherit.
- [ ] Accept the dependency cost: `controller-runtime` plus `client-go`, `k8s.io/api` and
      `apimachinery` take the module from 4 direct dependencies to roughly 8 direct and ~40
      indirect. Go has no test-only dependency scope, so they land in `go.mod` permanently and CI's
      `git diff --exit-code go.mod go.sum` will enforce it. The binary and the image are unaffected.
      `client-go` is already settled by [ADR 0006](./docs/adr/0006-echo-proxy-middleware-and-the-streaming-dependency.md);
      `controller-runtime` is new and wants an ADR, since ADR 0006 makes a point of the short list.
- [ ] Environment: free loopback ports, a writable temp dir, no root, no Docker. Linux and macOS
      only. ~5–15 s startup, so one control plane per suite, stopped in a `DeferCleanup` — a missed
      `Stop()` leaks `etcd` and `kube-apiserver` processes.

**What envtest does not run**, which shapes the specs: no kube-controller-manager, scheduler or
kubelet. Pods never start, garbage collection never runs, ServiceAccounts get no tokens, and
namespaces never leave `Terminating` — so specs use fresh namespace names rather than deleting and
waiting. None of it hurts: Mirage only cares about API paths.

**The wiring problem: credentials.** envtest authenticates its client with a TLS client certificate,
not a bearer token. Mirage forwards the `Authorization` header and holds no credentials of its own
([ADR 0001](./docs/adr/0001-mirage-never-adds-authority.md)), so wired naively every proxied request
arrives at the API server anonymous and gets a `401`.

- [ ] Preferred: a static token file via `--token-auth-file`, passed through
      `env.ControlPlane.APIServer.Configure()`. The test Client uses `BearerToken`, Mirage gets a
      CA-only TLS transport — exactly the production arrangement.
- [ ] Fallback: hand Mirage the cert-based transport from `rest.TransportFor(env.Config)`. Three
      lines shorter, but Mirage then carries credentials, so a header-forwarding regression becomes
      invisible — the opposite of what these tests are for.

**Structure**

- [ ] A package behind `//go:build integration`, so `just test` stays fast and the suite is
      invisible to `go tool ginkgo -r` without the tag. A `just test-integration` recipe runs
      `setup-envtest` and then the tagged suite.
- [ ] Per suite: control plane up, CRDs from `testdata` — one namespaced, one cluster-scoped —
      target and foreign namespaces created, Mirage started via `server.New` on a random loopback
      port with `Upstream` pointed at `env.Config.Host`. Unique object names per spec, because
      `just test` runs `--randomize-all`.
- [ ] **Do not guard the suite with `if KUBEBUILDER_ASSETS == "" { Skip() }`.** ADR 0006 exists
      because the streaming behaviour fails silently; a suite that skips itself when its binaries are
      missing reproduces that exact failure mode in the harness. Missing binaries should be a hard
      error.

**Specs worth writing**

- [ ] A real client-go informer over a Confined Resource receives ADD, UPDATE and DELETE. The
      non-optional one from ADR 0006 — the only thing standing between a Go or Echo upgrade and a
      silent production failure.
- [ ] A cluster-wide LIST returns only target-namespace objects while an object exists in the
      foreign namespace.
- [ ] Both watch spellings stream: `?watch=true` and the legacy `/watch/` prefix. Worth confirming a
      current API server still serves the legacy form at all.
- [ ] RBAC end to end: with the token's user bound to a `Role` in the target namespace only, a
      confined cluster-wide LIST succeeds where the same request straight to the API server gets a
      `403`. This is Mirage's whole reason for existing, proven rather than asserted.
- [ ] Masked Resource against a real client: the informer syncs cleanly, LIST is empty, a named GET
      `404`s, mutation `403`s, protobuf-only `Accept` gets `406`, and a WATCH closes at
      `timeoutSeconds` rather than hanging. ADR 0004's "client-go notices the difference".
- [ ] Pass Through: discovery survives, so a RESTMapper can resolve types through Mirage at all.

- [ ] **Document what the discovery path implies for masking.** Discovery is Passed Through, so a
      Masked Resource whose CRD is not installed Upstream does not appear in discovery either: a
      typed client's RESTMapper fails to resolve the GVR and errors before it ever issues a request
      Mirage could answer. Masking therefore only works when the CRD exists cluster-wide and the
      Deployer merely cannot read it — which is the Shipwright situation. The masked specs should
      install the CRD, and the README's Limitations section should say this.

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
