package decide_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/decide"
)

const targetNamespace = "tenant-a"

var _ = Describe("Decide", func() {
	var decider *decide.Decider

	BeforeEach(func() {
		decider = decide.New(config.Config{
			Confined: []config.Resource{
				{Group: "", Plural: "pods"},
				{Group: "shipwright.io", Plural: "builds"},
			},
			Masked: []config.Masked{
				{
					Resource: config.Resource{Group: "shipwright.io", Plural: "clusterbuildstrategies"},
					Kind:     "ClusterBuildStrategy",
				},
			},
		}, targetNamespace)
	})

	// outbound is the path expected Upstream; "" means the inbound path unchanged.
	DescribeTable("classifying a request",
		func(method, path string, wantAction decide.Action, outbound string) {
			got := decider.Decide(method, path)

			Expect(got.Action).To(Equal(wantAction))
			if wantAction != decide.Mask {
				want := outbound
				if want == "" {
					want = path
				}
				Expect(got.Path).To(Equal(want))
			}
		},

		Entry("inserts the Target Namespace into a cluster-wide collection of a confined core resource",
			http.MethodGet, "/api/v1/pods",
			decide.Confine, "/api/v1/namespaces/tenant-a/pods"),

		Entry("inserts the Target Namespace into a cluster-wide collection of a confined grouped resource",
			http.MethodGet, "/apis/shipwright.io/v1beta1/builds",
			decide.Confine, "/apis/shipwright.io/v1beta1/namespaces/tenant-a/builds"),

		// The watch prefix has to survive the rewrite. Without it the Client asked
		// for a stream and would get a single list instead.
		Entry("keeps the legacy watch prefix when inserting the Target Namespace",
			http.MethodGet, "/api/v1/watch/pods",
			decide.Confine, "/api/v1/watch/namespaces/tenant-a/pods"),

		Entry("keeps the legacy watch prefix on a grouped resource",
			http.MethodGet, "/apis/shipwright.io/v1beta1/watch/builds",
			decide.Confine, "/apis/shipwright.io/v1beta1/watch/namespaces/tenant-a/builds"),

		Entry("leaves a legacy watch that already names the Target Namespace alone",
			http.MethodGet, "/api/v1/watch/namespaces/tenant-a/pods",
			decide.PassThrough, ""),

		Entry("applies a config entry to every version of the resource",
			http.MethodGet, "/apis/shipwright.io/v1alpha1/builds",
			decide.Confine, "/apis/shipwright.io/v1alpha1/namespaces/tenant-a/builds"),

		Entry("leaves an explicit Target Namespace alone, it is already correct",
			http.MethodGet, "/apis/shipwright.io/v1beta1/namespaces/tenant-a/builds",
			decide.PassThrough, ""),

		Entry("leaves an explicit foreign namespace untouched, for the API server to refuse",
			http.MethodGet, "/apis/shipwright.io/v1beta1/namespaces/tenant-b/builds",
			decide.PassThrough, ""),

		Entry("leaves a single object untouched",
			http.MethodGet, "/apis/shipwright.io/v1beta1/namespaces/tenant-a/builds/my-build",
			decide.PassThrough, ""),

		Entry("leaves a subresource untouched",
			http.MethodPut, "/apis/shipwright.io/v1beta1/namespaces/tenant-a/builds/my-build/status",
			decide.PassThrough, ""),

		Entry("leaves a namespaced POST untouched",
			http.MethodPost, "/api/v1/namespaces/tenant-a/pods",
			decide.PassThrough, ""),

		Entry("passes an unconfigured resource through",
			http.MethodGet, "/apis/tekton.dev/v1/taskruns",
			decide.PassThrough, ""),

		Entry("answers a masked collection locally",
			http.MethodGet, "/apis/shipwright.io/v1beta1/clusterbuildstrategies",
			decide.Mask, ""),

		Entry("answers a named masked resource locally too",
			http.MethodGet, "/apis/shipwright.io/v1beta1/clusterbuildstrategies/buildah",
			decide.Mask, ""),

		Entry("passes discovery through",
			http.MethodGet, "/apis/shipwright.io/v1beta1",
			decide.PassThrough, ""),

		Entry("passes the API group list through",
			http.MethodGet, "/apis",
			decide.PassThrough, ""),

		Entry("passes non-API paths through",
			http.MethodGet, "/openapi/v2",
			decide.PassThrough, ""),

		Entry("passes the root path through",
			http.MethodGet, "/",
			decide.PassThrough, ""),

		// Parsed as Namespace tenant-a, subresource finalize. Passed Through
		// because it names a single object: even with "namespaces" configured as
		// confined, only a namespace-less collection is ever confined.
		Entry("passes a Namespace subresource through untouched",
			http.MethodPut, "/api/v1/namespaces/tenant-a/finalize",
			decide.PassThrough, ""),
	)

	Describe("the warning heuristic", func() {
		It("warns about a cluster-wide collection of an unconfigured resource", func() {
			// The signature of a resource missing from `confined`.
			Expect(decider.Decide(http.MethodGet, "/apis/tekton.dev/v1/taskruns").Warn).To(BeTrue())
		})

		It("stays quiet when the request already names a namespace", func() {
			Expect(decider.Decide(http.MethodGet, "/apis/tekton.dev/v1/namespaces/tenant-a/taskruns").Warn).To(BeFalse())
		})

		It("stays quiet when the resource is confined", func() {
			Expect(decider.Decide(http.MethodGet, "/apis/shipwright.io/v1beta1/builds").Warn).To(BeFalse())
		})

		It("warns about a legacy watch of an unconfigured cluster-wide collection", func() {
			// The same missing-entry signature, spelled the older way.
			Expect(decider.Decide(http.MethodGet, "/apis/tekton.dev/v1/watch/taskruns").Warn).To(BeTrue())
		})

		It("stays quiet for a named cluster-scoped object", func() {
			Expect(decider.Decide(http.MethodGet, "/api/v1/nodes/node-1").Warn).To(BeFalse())
		})

		It("stays quiet for a Namespace subresource", func() {
			// It names a single object, so it is not the cluster-wide collection
			// request the heuristic is looking for.
			Expect(decider.Decide(http.MethodGet, "/api/v1/namespaces/tenant-a/finalize").Warn).To(BeFalse())
		})

		It("stays quiet for the namespaces collection", func() {
			// It has the shape the heuristic looks for, but `confined` is not the fix
			// — namespaces is cluster-scoped, and config.Validate refuses it. Warning
			// here would point the Deployer at a configuration Mirage will not start
			// with.
			Expect(decider.Decide(http.MethodGet, "/api/v1/namespaces").Warn).To(BeFalse())
		})
	})

	It("refuses to confine namespaces even when told to confine them", func() {
		// config.Validate rejects this configuration, so it can only arise from a
		// Decider built directly. The guard is what keeps the invariant local to
		// Decide rather than dependent on validation having run.
		d := decide.New(config.Config{
			Confined: []config.Resource{{Plural: "namespaces"}},
		}, targetNamespace)

		got := d.Decide(http.MethodGet, "/api/v1/namespaces")

		Expect(got.Action).To(Equal(decide.PassThrough))
		Expect(got.Path).To(Equal("/api/v1/namespaces"))
	})

	It("carries the configured Kind on a Mask decision", func() {
		// Mirage needs the Kind to name the empty list it synthesises.
		got := decider.Decide(http.MethodGet, "/apis/shipwright.io/v1beta1/clusterbuildstrategies")
		Expect(got.Masked.Kind).To(Equal("ClusterBuildStrategy"))
	})
})

var _ = Describe("Parse", func() {
	DescribeTable("breaking a path into its parts",
		func(path string, want decide.Target) {
			Expect(decide.Parse(path)).To(Equal(want))
		},

		Entry("a cluster-wide core collection", "/api/v1/pods",
			decide.Target{Version: "v1", Plural: "pods", OK: true}),

		Entry("the namespaces collection itself", "/api/v1/namespaces",
			decide.Target{Version: "v1", Plural: "namespaces", OK: true}),

		Entry("a single Namespace object, not a resource within a namespace", "/api/v1/namespaces/tenant-a",
			decide.Target{Version: "v1", Plural: "namespaces", Name: "tenant-a", OK: true}),

		Entry("a cluster-scoped object", "/api/v1/nodes/node-1",
			decide.Target{Version: "v1", Plural: "nodes", Name: "node-1", OK: true}),

		Entry("a cluster-scoped subresource spanning several segments", "/api/v1/nodes/node-1/proxy/metrics",
			decide.Target{
				Version: "v1", Plural: "nodes", Name: "node-1",
				Subresource: "proxy/metrics", OK: true,
			}),

		Entry("a namespaced collection", "/api/v1/namespaces/tenant-a/pods",
			decide.Target{Version: "v1", Namespace: "tenant-a", Plural: "pods", Namespaced: true, OK: true}),

		Entry("a namespaced object", "/api/v1/namespaces/tenant-a/pods/nginx",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Plural: "pods", Name: "nginx",
				Namespaced: true, OK: true,
			}),

		Entry("a subresource", "/api/v1/namespaces/tenant-a/pods/nginx/log",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Plural: "pods", Name: "nginx",
				Subresource: "log", Namespaced: true, OK: true,
			}),

		Entry("a namespaced subresource spanning several segments", "/api/v1/namespaces/tenant-a/pods/nginx/proxy/healthz",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Plural: "pods", Name: "nginx",
				Subresource: "proxy/healthz", Namespaced: true, OK: true,
			}),

		Entry("a grouped collection", "/apis/shipwright.io/v1beta1/builds",
			decide.Target{Group: "shipwright.io", Version: "v1beta1", Plural: "builds", OK: true}),

		// The three entries below share a shape — /api/v1/namespaces/{x}/{y} — and
		// are told apart only by whether y is a subresource the Namespace object
		// registers. They are pinned together because that is the whole of the rule.
		Entry("the finalize subresource of a Namespace, not a resource within it",
			"/api/v1/namespaces/tenant-a/finalize",
			decide.Target{
				Version: "v1", Plural: "namespaces", Name: "tenant-a",
				Subresource: "finalize", OK: true,
			}),

		Entry("the status subresource of a Namespace", "/api/v1/namespaces/tenant-a/status",
			decide.Target{
				Version: "v1", Plural: "namespaces", Name: "tenant-a",
				Subresource: "status", OK: true,
			}),

		Entry("a resource whose name merely resembles one", "/api/v1/namespaces/tenant-a/events",
			decide.Target{Version: "v1", Namespace: "tenant-a", Plural: "events", Namespaced: true, OK: true}),

		// One segment further and the subresource reading no longer applies: a
		// Namespace has no sub-subresources, so this is an object within tenant-a.
		Entry("a longer path starting with a subresource name", "/api/v1/namespaces/tenant-a/status/foo",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Plural: "status", Name: "foo",
				Namespaced: true, OK: true,
			}),

		// The legacy watch prefix sits between the version and the shapes above, so
		// each of these is one of them with Watch set.
		Entry("a legacy watch of a cluster-wide collection", "/api/v1/watch/pods",
			decide.Target{Version: "v1", Plural: "pods", Watch: true, OK: true}),

		Entry("a legacy watch of a namespaced collection", "/api/v1/watch/namespaces/tenant-a/pods",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Plural: "pods",
				Namespaced: true, Watch: true, OK: true,
			}),

		Entry("a legacy watch of a single Namespace object", "/api/v1/watch/namespaces/tenant-a",
			decide.Target{
				Version: "v1", Plural: "namespaces", Name: "tenant-a",
				Watch: true, OK: true,
			}),

		Entry("a legacy watch of a grouped collection", "/apis/shipwright.io/v1beta1/watch/builds",
			decide.Target{
				Group: "shipwright.io", Version: "v1beta1", Plural: "builds",
				Watch: true, OK: true,
			}),

		Entry("the watch prefix with nothing after it", "/api/v1/watch", decide.Target{}),

		Entry("core discovery", "/api/v1", decide.Target{}),
		Entry("group discovery", "/apis/shipwright.io/v1beta1", decide.Target{}),
		Entry("the API group list", "/apis", decide.Target{}),
		Entry("a group without a version", "/apis/shipwright.io", decide.Target{}),
		Entry("a non-API path", "/healthz", decide.Target{}),
		Entry("the root path", "/", decide.Target{}),

		// An empty segment leaves no resource to name, so there is nothing to
		// decide about and the path goes Upstream untouched.
		Entry("a path with an empty segment", "/api/v1//pods", decide.Target{}),
		Entry("an empty segment where the resource belongs", "/api/v1/namespaces/tenant-a//pods", decide.Target{}),
	)
})
