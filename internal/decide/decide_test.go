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
			Handled: []config.Resource{
				{Group: "", Resource: "pods"},
				{Group: "shipwright.io", Resource: "builds"},
			},
			Masked: []config.Masked{
				{
					Resource: config.Resource{Group: "shipwright.io", Resource: "clusterbuildstrategies"},
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

		Entry("inserts the Target Namespace into a cluster-wide collection of a handled core resource",
			http.MethodGet, "/api/v1/pods",
			decide.Rewrite, "/api/v1/namespaces/tenant-a/pods"),

		Entry("inserts the Target Namespace into a cluster-wide collection of a handled grouped resource",
			http.MethodGet, "/apis/shipwright.io/v1beta1/builds",
			decide.Rewrite, "/apis/shipwright.io/v1beta1/namespaces/tenant-a/builds"),

		Entry("applies a config entry to every version of the resource",
			http.MethodGet, "/apis/shipwright.io/v1alpha1/builds",
			decide.Rewrite, "/apis/shipwright.io/v1alpha1/namespaces/tenant-a/builds"),

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
		// handled, only a namespace-less collection is ever rewritten.
		Entry("passes a Namespace subresource through untouched",
			http.MethodPut, "/api/v1/namespaces/tenant-a/finalize",
			decide.PassThrough, ""),
	)

	Describe("the warning heuristic", func() {
		It("warns about a cluster-wide collection of an unconfigured resource", func() {
			// The signature of a resource missing from `handled`.
			Expect(decider.Decide(http.MethodGet, "/apis/tekton.dev/v1/taskruns").Warn).To(BeTrue())
		})

		It("stays quiet when the request already names a namespace", func() {
			Expect(decider.Decide(http.MethodGet, "/apis/tekton.dev/v1/namespaces/tenant-a/taskruns").Warn).To(BeFalse())
		})

		It("stays quiet when the resource is handled", func() {
			Expect(decider.Decide(http.MethodGet, "/apis/shipwright.io/v1beta1/builds").Warn).To(BeFalse())
		})

		It("stays quiet for a named cluster-scoped object", func() {
			Expect(decider.Decide(http.MethodGet, "/api/v1/nodes/node-1").Warn).To(BeFalse())
		})

		It("stays quiet for a Namespace subresource", func() {
			// It names a single object, so it is not the cluster-wide collection
			// request the heuristic is looking for.
			Expect(decider.Decide(http.MethodGet, "/api/v1/namespaces/tenant-a/finalize").Warn).To(BeFalse())
		})
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
			decide.Target{Version: "v1", Resource: "pods", OK: true}),

		Entry("the namespaces collection itself", "/api/v1/namespaces",
			decide.Target{Version: "v1", Resource: "namespaces", OK: true}),

		Entry("a single Namespace object, not a resource within a namespace", "/api/v1/namespaces/tenant-a",
			decide.Target{Version: "v1", Resource: "namespaces", Name: "tenant-a", OK: true}),

		Entry("a namespaced collection", "/api/v1/namespaces/tenant-a/pods",
			decide.Target{Version: "v1", Namespace: "tenant-a", Resource: "pods", Namespaced: true, OK: true}),

		Entry("a subresource", "/api/v1/namespaces/tenant-a/pods/nginx/log",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Resource: "pods", Name: "nginx",
				Subresource: "log", Namespaced: true, OK: true,
			}),

		Entry("a grouped collection", "/apis/shipwright.io/v1beta1/builds",
			decide.Target{Group: "shipwright.io", Version: "v1beta1", Resource: "builds", OK: true}),

		// The three entries below share a shape — /api/v1/namespaces/{x}/{y} — and
		// are told apart only by whether y is a subresource the Namespace object
		// registers. They are pinned together because that is the whole of the rule.
		Entry("the finalize subresource of a Namespace, not a resource within it",
			"/api/v1/namespaces/tenant-a/finalize",
			decide.Target{
				Version: "v1", Resource: "namespaces", Name: "tenant-a",
				Subresource: "finalize", OK: true,
			}),

		Entry("the status subresource of a Namespace", "/api/v1/namespaces/tenant-a/status",
			decide.Target{
				Version: "v1", Resource: "namespaces", Name: "tenant-a",
				Subresource: "status", OK: true,
			}),

		Entry("a resource whose name merely resembles one", "/api/v1/namespaces/tenant-a/events",
			decide.Target{Version: "v1", Namespace: "tenant-a", Resource: "events", Namespaced: true, OK: true}),

		// One segment further and the subresource reading no longer applies: a
		// Namespace has no sub-subresources, so this is an object within tenant-a.
		Entry("a longer path starting with a subresource name", "/api/v1/namespaces/tenant-a/status/foo",
			decide.Target{
				Version: "v1", Namespace: "tenant-a", Resource: "status", Name: "foo",
				Namespaced: true, OK: true,
			}),

		Entry("a group without a version", "/apis/shipwright.io", decide.Target{}),
		Entry("a non-API path", "/healthz", decide.Target{}),
		Entry("the root path", "/", decide.Target{}),
	)
})
