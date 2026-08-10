# envtest for the integration tier, and the dependencies it costs

The unit tier tests `decide` as a pure function, `mask` against `httptest`, and the server's
streaming contract by assertion. None of that can tell us whether a real client-go informer receives
events through Mirage, which [ADR 0006](./0006-echo-proxy-middleware-and-the-streaming-dependency.md)
calls non-optional. The integration tier runs Mirage against a real Kubernetes API server, using
`sigs.k8s.io/controller-runtime/pkg/envtest`.

`envtest` starts real `etcd` and `kube-apiserver` binaries as child processes on loopback and hands
back a `*rest.Config`. It is not a fake and not a container.

**Why not a fake.** Mirage's entire job is URL manipulation against a real API server's routing —
which paths exist, which spellings of WATCH are served, what a `metav1.Status` has to look like for
client-go to classify it. A fake would answer those questions with whatever we already believe about
them, which is precisely the belief under test. The same objection rules out `client-go`'s
`fake.Clientset`: it never parses a URL.

**Why not kind or k3s.** Both want Docker or a privileged runtime. `envtest` wants a writable temp
dir, some free loopback ports, and no root, which is a much cheaper thing to ask of a laptop and of
CI. What we give up is the parts `envtest` does not run — no kube-controller-manager, scheduler or
kubelet, so Pods never start, garbage collection never runs, ServiceAccounts get no tokens, and a
deleted namespace never leaves `Terminating`. None of it matters here: Mirage only cares about API
paths. It does shape the specs, which use fresh names rather than deleting namespaces and waiting.

## The credential wiring

`envtest` authenticates its own client with a TLS client certificate. Mirage forwards the
`Authorization` header and holds no credentials of its own
([ADR 0001](./0001-mirage-never-adds-authority.md)), so wired naively every proxied request would
arrive at the API server anonymous and come back `401`.

The suite therefore starts the API server with `--token-auth-file`, passed through
`env.ControlPlane.GetAPIServer().Configure()`, and gives the test Client a bearer token. Mirage gets
a CA-only transport built with `rest.TransportFor(rest.AnonymousClientConfig(env.Config))` — no
client certificate, no token. That is exactly the production arrangement, and it means a
header-forwarding regression fails the suite loudly.

The rejected alternative was to hand Mirage the cert-based transport from
`rest.TransportFor(env.Config)`. Three lines shorter, and it would have made Mirage carry
credentials — so header forwarding could break without a single spec noticing, which is the opposite
of what these tests are for.

The suite also sets `--authorization-mode=RBAC` explicitly rather than inheriting `envtest`'s
default. Under `AlwaysAllow` the RBAC spec — a cluster-wide LIST that is `403` straight to the API
server and succeeds through Mirage — would pass while proving nothing.

## The Kubernetes version

Pinned to **1.34.1**, in `justfile`'s `k8s_version`. The README claims 1.29+, so the version the
suite actually proves is a choice worth recording rather than a default worth inheriting. It is the
current stable line, which is where the legacy `/watch/` prefix is most likely to have been dropped
— the spec asserting it still works is only interesting against a current server.

Raising the floor claim, or proving it, would mean a CI matrix over two versions. Not done; see
`TODO.md`.

## The dependency cost, accepted

`controller-runtime` pulls in `client-go`, `k8s.io/api`, `k8s.io/apimachinery` and their transitive
set, taking the module from 4 direct dependencies to 8, and to 56 indirect.

Worth knowing what that number is not: only 9 of the 56 are `k8s.io` or `sigs.k8s.io` modules. The
rest is the serialisation, logging and protobuf machinery underneath them. `go mod tidy` also
*dropped* `fsnotify` and `gomodules.xyz/jsonpatch` on the way in, which is a useful signal — they
serve `controller-runtime`'s manager and webhook packages, and nothing here imports those. The
suite's reach into `controller-runtime` is `pkg/envtest` and nothing else.

ADR 0006 makes a point of the short list, so this needs saying plainly: **Go has no test-only
dependency scope.** These land in `go.mod` permanently, and CI's `git diff --exit-code go.mod
go.sum` will enforce them. The binary and the image are unaffected — nothing under `cmd/` or
`internal/` imports any of it, the integration package is behind a build tag, and the static binary
is byte-identical either way.

`client-go` was already settled by ADR 0006 as the standard against which Mirage's imitation is
judged. `controller-runtime` is new, and it earns its place by being the only maintained way to get
a real API server without a container runtime. `setup-envtest` is wired as a `tool` directive in
`go.mod`, matching how `ginkgo` is already handled, so `go tool setup-envtest` works with nothing
installed by hand.

## No skip guard

The suite does **not** check `KUBEBUILDER_ASSETS` and `Skip()` when it is empty. ADR 0006 exists
because the streaming behaviour fails silently; a suite that quietly skips itself when its binaries
are missing reproduces that exact failure mode inside the harness, and the one test standing between
an Echo upgrade and a silent production outage would stop running with a green tick. Missing
binaries are a hard error.

The tag is what keeps `just test` fast — not the skip.

## The tag alone is not enough, and that is not obvious

A build tag governs compilation, not discovery, and three tools discover before they compile:

- `ginkgo -r` finds suites by scanning for `_test.go` **filenames**, so it finds `test/integration`,
  tries to build it, and fails with "build constraints exclude all Go files". `just test` therefore
  passes `--skip-package=test/integration`.
- `go vet ./...` and `go build ./...` treat a directory whose every Go file is excluded as an error
  rather than a silent skip. `test/integration/doc.go` carries no tag and declares nothing, purely
  so the package always has one buildable file.

Neither is guesswork worth repeating: both were found by running the commands. If the suite ever
moves, both need moving with it.

The tagged files are still vetted — `just vet` runs `go vet -tags=integration ./test/integration`
as a second step, because vetting needs no envtest binaries and the alternative is a package nothing
lints until someone runs it.
