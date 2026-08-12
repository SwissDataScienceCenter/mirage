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

## The plaintext listener cost the Client its credentials

Found in production against the Shipwright build controller, 2026-08-12. Fixed by
[ADR 0002](./docs/adr/0002-self-signed-tls-on-loopback.md), which now records the opposite of what
it originally did. Written up at length because almost every step of the diagnosis was misleading,
and the next person deserves the map.

**The symptom.** The controller died at startup with one line, and nothing else anywhere:

```
failed to determine if *v1.Pod is namespaced: failed to get restmapping: failed to get server groups: unknown
```

**The cause.** `clientcmd` reads a kubeconfig's credentials only when the server URL is `https://`
— `DirectClientConfig.ClientConfig()` gates the whole block behind
`restclient.IsConfigTransportTLS`, which is just `baseURL.Scheme == "https"`. Mirage served
plaintext and the published kubeconfig therefore said `server: http://127.0.0.1:8001`, so `token`,
`tokenFile` and client certificates were all skipped, silently. The Client's `rest.Config` had no
bearer token, its requests arrived at the API server as `system:anonymous`, and `GET /api` came back
`403`.

**Why the error said nothing.** `setDiscoveryDefaults` gives the discovery client an inert
serializer:

```go
codec := runtime.NoopEncoder{Decoder: scheme.Codecs.UniversalDecoder()}
config.NegotiatedSerializer = serializer.NegotiatedSerializerWrapper(runtime.SerializerInfo{Serializer: codec})
```

That `SerializerInfo` has no `MediaType`, so `SerializerInfoForMediaType` can never match
`application/json` and `rest.Request.transformResponse` fails to negotiate a decoder for *any*
discovery response. On a non-2xx it falls through to `transformUnstructuredResponseError`, where
`message` starts as the literal string `"unknown"` and is replaced by the body only when the
`Content-Type` is `text/*`. So the API server's entirely clear
`forbidden: User "system:anonymous" cannot get path "/api"` was decoded, discarded and replaced with
one word. **Any** discovery failure reads as `unknown`, whatever its cause — the message carries no
information at all, which is worth knowing before spending an evening on it.

**What made it hard, and what to do differently.** Every manual replay succeeded: `curl` from a
debug container in the same Pod, with the same token from the same projected volume, got `200`.
Only requests originating from the Client failed, because only they went through `clientcmd`. Three
separate wrong theories — TLS to the API server, aggregated-discovery content negotiation, a
startup-window race — each survived longer than they should have because Mirage recorded only its
own intent and never the outcome. Fixed by [the observability work below](#observability); the
`decided` line now carries whether the request even had an `Authorization` header, which is the
single fact that would have ended it immediately.

### Alternatives considered

- **Self-signed cert, `insecure-skip-tls-verify: true` in the kubeconfig.** Taken. Generated per
  start, held in memory, never distributed, nothing to rotate. Verification is skipped because it
  could not be worth anything: the peer is another container in the same network namespace, and
  anything positioned to intercept the connection already holds the token.
- **Mirage writes its CA and the whole kubeconfig into a shared `emptyDir`.** Rejected for now, but
  the better design in one respect: it deletes the hand-maintained kubeconfig from the ConfigMap,
  where it can drift out of step with the listener — which is precisely how this bug shipped. Costs
  a static reviewable ConfigMap in exchange for a file generated at runtime. Revisit if the drift
  recurs.
- **Inline `token:` in the kubeconfig instead of `tokenFile:`.** Does not work. The gate skips the
  entire credential block, so an inline token is dropped exactly as the file was.
- **Have Mirage inject an `Authorization` header of its own.** Rejected outright: it violates
  [ADR 0001](./docs/adr/0001-mirage-never-adds-authority.md), which is the property the whole design
  rests on. Mirage would have to hold a credential, and a broken Mirage could then grant access the
  Client's ServiceAccount does not have.
- **Leave plaintext, document that Clients must set `insecure-skip-tls-verify` against an `http://`
  server.** Not possible — the scheme is the gate, and no kubeconfig setting opens it.

### Left over

- [ ] **Nothing tests the credential path end to end.** `test/integration/suite_test.go` serves
      Mirage with `httptest.NewServer` and builds `mirageCfg` programmatically, so `clientcmd` never
      runs and the scheme never mattered. `internal/server/kubeconfig_test.go` now pins the
      `clientcmd` behaviour directly, but a spec that loads the README's kubeconfig against a TLS
      Mirage and drives a real informer through it would be the one that cannot pass vacuously.
      Related to the `echo.StartConfig` gap already noted below — both are the same hole, that the
      suite tests `server.New` rather than the program.

- [ ] **The README's `startupProbe` cannot pass as written.** `httpGet` is sent by the kubelet to
      the Pod IP, and Mirage binds `127.0.0.1` only, so `<podIP>:8001` is refused and the native
      sidecar never reports started. It needs `host: 127.0.0.1` alongside `scheme: HTTPS`. Found
      while reading the manifest during this diagnosis; not the cause of it, and untested, since the
      cluster it was found on evidently runs a manifest that differs here.

<a id="observability"></a>
## Observability

Added while diagnosing the credential bug above, and kept because its absence is what made that
diagnosis take as long as it did.

- [x] **`ErrorHandler` on the proxy middleware.** Echo installs its own handler on the underlying
      `httputil.ReverseProxy`, which suppresses the stdlib's `http: proxy error` line, and then
      answers `502` with a JSON body that is not a `metav1.Status` — which client-go also renders as
      `unknown`. A failure to reach Upstream was invisible from both ends.
- [x] **`Logging` and `Recover` as global middleware.** `Logging` carries the response status, which
      neither the `decided` line nor the Client can supply. Global rather than route-level so that
      responses Echo generates before routing — a `404`, a method mismatch — are logged at all;
      route-level middleware never runs for those. `Recover` must sit *inside* `Logging`, since Echo
      v5's `Recover` does not log: it converts the panic into a `PanicStackError` and returns it, so
      only a logger reading `v.Error` records it. On its own it would be a regression, swallowing
      the stack trace `http.Server` prints today.
- [x] **Whether the request carried an `Authorization` header**, as a boolean on the `decided` line.
      Never the value — Mirage holds no credentials and this keeps it that way.

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
- [ ] Support for Clients that call `rest.InClusterConfig()` directly and therefore ignore
      `KUBECONFIG`. Mirage now serves TLS, but `InClusterConfig` hardcodes the CA bundle path, so
      this additionally needs a projected volume overriding `ca.crt` — and therefore a CA to
      distribute, which the self-signed mode deliberately avoids.
      See [ADR 0002](./docs/adr/0002-self-signed-tls-on-loopback.md).
- [ ] Masking `SelfSubjectAccessReview` to always answer `allowed: true`, for Clients that check
      their own permissions at startup and refuse to run. Shipwright does not, so this is unbuilt.
      It would not violate [ADR 0001](./docs/adr/0001-mirage-never-adds-authority.md) — the API
      server still enforces the real request — but it should be opt-in per config, never always-on.
