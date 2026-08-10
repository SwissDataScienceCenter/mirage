# TODO

Open items from the design session. The design itself is settled — see
[`CONTEXT.md`](./CONTEXT.md) and [`docs/adr/`](./docs/adr/).

## Open questions

**Resolved: the token file is the wiring.** The suite appends `--token-auth-file` through
`env.ControlPlane.GetAPIServer().Configure()` and the Client uses a bearer token, so Mirage gets the
CA-only transport it has in production. The mTLS fallback is not needed and was not taken — it would
have cost the header-forwarding coverage. Recorded in
[ADR 0007](./docs/adr/0007-envtest-for-the-integration-tier.md).

**Resolved: build tag, plus its own CI job.** `//go:build integration` and `just test-integration`,
so `just test` stays fast and needs no binaries. Missing binaries are a hard error, never a skip —
same reasoning as ADR 0006.

**Resolved: CI can fetch the envtest binaries.** `ubuntu-latest`, no privileges, no Docker — just
network access to the `controller-tools` GitHub releases index, plus an `actions/cache` step keyed on
the pinned Kubernetes version. The old GCS bucket is gone; `setup-envtest` reads the releases index
now.

**Resolved: pinned to Kubernetes 1.34.1**, in the `justfile`'s `k8s_version`. See ADR 0007 for why
the current line rather than the 1.29 floor the README claims.

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

Both tiers are built and covered by Ginkgo suites.

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
- [x] End-to-end test running a real client-go informer through Mirage — required, not optional.
      `test/integration/informer_test.go`.

## Integration tests with envtest

Built. `test/integration`, behind `//go:build integration`, run with `just test-integration`. The
design and its trade-offs are recorded in
[ADR 0007](./docs/adr/0007-envtest-for-the-integration-tier.md); what follows is only what is left.

- [x] The binaries, via a `tool sigs.k8s.io/controller-runtime/tools/setup-envtest` directive in
      `go.mod` and a `just envtest` recipe that prints the asset path.
- [x] Kubernetes pinned to 1.34.1 in the `justfile`.
- [x] The dependency cost accepted and written down. `controller-runtime` and `client-go` are in
      `go.mod` permanently; `cmd/` and `internal/` import none of it, so the binary and image are
      unchanged.
- [x] The credential wiring: `--token-auth-file` on the API server, a bearer token in the Client, a
      CA-only credential-free transport in Mirage.
- [x] `--authorization-mode=RBAC` set rather than inherited, so the RBAC spec cannot pass vacuously.
- [x] No `KUBEBUILDER_ASSETS == "" { Skip() }` guard. Missing binaries are a hard error.
- [x] The specs: informer ADD/UPDATE/DELETE over a Confined Resource; cluster-wide LIST confined to
      the Target Namespace; both watch spellings; explicit foreign namespace left untouched; RBAC end
      to end, both directions; Masked Resource LIST/GET/mutation/protobuf/watch-timeout/informer-sync;
      discovery and RESTMapper resolution through the proxy.
- [x] The discovery-implies-masking point documented in the README's Limitations.

**Left over**

- [ ] **The suite serves Mirage with `httptest.NewServer`, not `cmd/mirage/main.go`'s
      `echo.StartConfig`.** So `server.New` is covered but the `http.Server` settings are not — and
      `ReadTimeout = 0` / `WriteTimeout = 0` are there for exactly the ADR 0006 reason, to stop a
      whole-request timeout silently cutting off long-lived WATCHes. Someone setting `WriteTimeout`
      back to 30s in `main.go` would break production and pass this suite. Extracting the
      `StartConfig` into `server` so both callers share one definition would close it.

- [ ] **Legacy `/watch/` prefix: confirm the assertion is real, not vacuous.** The spec asserts a
      `200` and a streamed object. If a future API server drops the form, the failure message says
      so — but check on the first run that 1.34 actually serves it rather than quietly answering
      with a plain list.

- [ ] **Consider a `mirage.test` group name collision check.** The two test CRDs live in a group a
      real cluster will not have. Harmless, but if the suite ever runs against a shared cluster the
      CRD install is cluster-wide and destructive. Not a concern for envtest, which is a fresh
      control plane every run.

## CI for the integration tier

Local first, by decision. The job below is designed and not yet written — it goes in
`.github/workflows/ci.yml` next to `test`, `lint` and `image`.

- [ ] A third `integration` job on `ubuntu-latest`: `actions/checkout`, `actions/setup-go` with
      `go-version-file: go.mod`, `extractions/setup-just`, then `just test-integration`. No Docker,
      no privileges — `setup-envtest` fetches from the `controller-tools` GitHub releases index over
      plain network access.

- [ ] **Cache the control-plane binaries.** `actions/cache` on
      `~/.local/share/kubebuilder-envtest`, keyed on the runner OS and the pinned Kubernetes version
      — `envtest-${{ runner.os }}-1.34.1`. Without it every run downloads ~150 MB. The key must
      carry the version, or bumping `k8s_version` silently reuses the old binaries and the job
      tests a version nobody chose. Read the version out of the `justfile` rather than repeating it
      in the workflow, so there is one place to change.

- [ ] **Decide whether it gates merges.** It is slower than `test` — binary download on a cold cache
      plus 5–15 s of control-plane startup — but the ADR 0006 informer spec is the one test whose
      absence is invisible. Blocking is the point of writing it.

- [ ] **`just tidy` must run with the tag in view.** `go mod tidy` considers all build constraints,
      so the tagged package keeps `controller-runtime` and `client-go` pinned. Worth confirming on
      the existing "Verify go.mod is tidy" step rather than discovering it as a red diff.

- [ ] **macOS runner?** The suite works on Linux and macOS. Nothing needs it yet, and it doubles the
      binary download; revisit if anyone develops on a Mac and hits a difference.

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
