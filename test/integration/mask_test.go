//go:build integration

package integration_test

import (
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// The Masked Resource specs answer ADR 0004's claim that "client-go notices the
// difference" between a real API server and something merely returning an empty
// list. The only way to test that claim is to point a real client at Mirage.
//
// The ClusterWidget CRD is installed Upstream and holds real objects. The test
// user is never granted access to it — which is the Shipwright situation exactly:
// the resource exists cluster-wide and the Deployer cannot read it.
var _ = Describe("A Masked Resource", func() {
	var existing string

	BeforeEach(func(ctx SpecContext) {
		// A real object Upstream, so an empty answer proves Mirage is answering
		// rather than the collection simply being empty.
		existing = uniqueName("real-clusterwidget")
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": clusterWidgetGVR.Group + "/" + clusterWidgetGVR.Version,
			"kind":       "ClusterWidget",
			"metadata":   map[string]any{"name": existing},
		}}
		_, err := adminDynamic.Resource(clusterWidgetGVR).Create(ctx, obj, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func(ctx SpecContext) {
			err := adminDynamic.Resource(clusterWidgetGVR).Delete(ctx, existing, metav1.DeleteOptions{})
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())
		})
	})

	It("LISTs as empty even though objects exist Upstream", func(ctx SpecContext) {
		list, err := mirageClient().Resource(clusterWidgetGVR).List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(list.Items).To(BeEmpty())
		Expect(list.GetKind()).To(Equal("ClusterWidgetList"))

		// The object really is there — the admin can see it. So the empty list came
		// from Mirage, not from an empty cluster.
		fromUpstream, err := adminDynamic.Resource(clusterWidgetGVR).List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(fromUpstream.Items).NotTo(BeEmpty())
	})

	It("404s a named GET, for an object that does exist Upstream", func(ctx SpecContext) {
		_, err := mirageClient().Resource(clusterWidgetGVR).Get(ctx, existing, metav1.GetOptions{})
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"client-go did not recognise the 404 as a NotFound; the metav1.Status is malformed: %v", err)
	})

	It("403s a mutation", func(ctx SpecContext) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": clusterWidgetGVR.Group + "/" + clusterWidgetGVR.Version,
			"kind":       "ClusterWidget",
			"metadata":   map[string]any{"name": uniqueName("rejected")},
		}}
		_, err := mirageClient().Resource(clusterWidgetGVR).Create(ctx, obj, metav1.CreateOptions{})
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsForbidden(err)).To(BeTrue(),
			"client-go did not recognise the 403 as Forbidden: %v", err)
	})

	It("406s a protobuf-only Accept, as a real API server does for a CRD", func(ctx SpecContext) {
		resp := through(ctx, http.MethodGet, "/apis/mirage.test/v1/clusterwidgets",
			http.Header{"Accept": []string{"application/vnd.kubernetes.protobuf"}})
		Expect(resp.StatusCode).To(Equal(http.StatusNotAcceptable))
	})

	It("closes a WATCH at timeoutSeconds rather than hanging", func(ctx SpecContext) {
		resp := through(ctx, http.MethodGet,
			"/apis/mirage.test/v1/clusterwidgets?watch=true&timeoutSeconds=2", nil)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		closed := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(closed)
			_, _ = io.Copy(io.Discard, resp.Body)
		}()

		// Generously more than the two seconds asked for, but far less than the
		// forever a hanging watch would take. An informer that never sees its watch
		// close cannot re-list, so this is not cosmetic.
		Eventually(closed, 15*time.Second).Should(BeClosed(),
			"the masked WATCH ignored timeoutSeconds and stayed open")
	})

	It("syncs a client-go informer cleanly", func(ctx SpecContext) {
		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
			mirageClient(), 0, metav1.NamespaceAll, nil)
		informer := factory.ForResource(clusterWidgetGVR).Informer()

		seen := make(chan string, 16)
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if u, ok := obj.(*unstructured.Unstructured); ok {
					seen <- u.GetName()
				}
			},
		})
		Expect(err).NotTo(HaveOccurred())

		stop := make(chan struct{})
		DeferCleanup(func() { close(stop) })
		go informer.Run(stop)

		// The point of ADR 0004's imitation work. A LIST at resourceVersion 1
		// followed by a WATCH from 1 has to satisfy the reflector, or the informer
		// spins on "too old resource version" and never reports itself synced.
		Expect(cache.WaitForCacheSync(stop, informer.HasSynced)).To(BeTrue(),
			"the informer never synced against the masked resource")

		Consistently(seen, 2*time.Second).ShouldNot(Receive(),
			"the masked resource delivered an object; it is meant to be empty")
	})
})
