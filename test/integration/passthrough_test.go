//go:build integration

package integration_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/restmapper"
)

var _ = Describe("Pass Through", func() {
	It("serves discovery, so a RESTMapper can resolve types through Mirage", func() {
		// Not a detail. Every typed client resolves a Kind to a resource path
		// through discovery before it issues a single request, so if discovery does
		// not survive the proxy then nothing else in this suite could have worked —
		// and a real Client would fail at startup with an error about the type
		// rather than about the proxy.
		dc, err := discovery.NewDiscoveryClientForConfig(mirageCfg)
		Expect(err).NotTo(HaveOccurred())

		groups, err := restmapper.GetAPIGroupResources(dc)
		Expect(err).NotTo(HaveOccurred())
		mapper := restmapper.NewDiscoveryRESTMapper(groups)

		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: "mirage.test", Kind: "Widget"}, "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(mapping.Resource).To(Equal(widgetGVR))
		Expect(mapping.Scope.Name()).To(Equal(meta.RESTScopeNameNamespace))

		// The Masked Resource resolves too, because discovery is Passed Through and
		// the CRD is installed Upstream. This is what makes masking work at all:
		// were the CRD absent, the RESTMapper would fail here and the Client would
		// error before ever issuing a request Mirage could answer. See the
		// Limitations section of the README.
		masked, err := mapper.RESTMapping(schema.GroupKind{Group: "mirage.test", Kind: "ClusterWidget"}, "v1")
		Expect(err).NotTo(HaveOccurred())
		Expect(masked.Resource).To(Equal(clusterWidgetGVR))
	})

	It("leaves an unconfigured resource alone", func(ctx SpecContext) {
		// Namespaces are neither confined nor masked, so the request reaches the
		// API server exactly as sent — and is refused, because the test user has no
		// cluster-wide permissions. Mirage passing it through unchanged is the
		// assertion; the 403 is how we can see it did.
		resp := through(ctx, http.MethodGet, "/api/v1/namespaces", nil)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})

	It("answers /healthz itself", func(ctx SpecContext) {
		resp := through(ctx, http.MethodGet, "/healthz", nil)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("passes a namespaced request through untouched", func(ctx SpecContext) {
		name := uniqueName("namespaced")
		createWidget(ctx, targetNamespace, name)

		// The path already names the Target Namespace. Confining is a no-op here by
		// construction, and this pins that Mirage does not double-insert it into
		// something like /namespaces/mirage-target/namespaces/mirage-target/widgets.
		got, err := mirageClient().Resource(widgetGVR).Namespace(targetNamespace).
			Get(ctx, name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.GetName()).To(Equal(name))
	})
})
