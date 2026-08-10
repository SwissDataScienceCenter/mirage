//go:build integration

package integration_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// event is one informer callback, flattened to something a channel assertion can
// read.
type event struct {
	verb string
	name string
}

func (e event) String() string { return e.verb + " " + e.name }

// watchWidgets runs a cluster-wide dynamic informer over Widgets through Mirage
// and returns a channel of its callbacks, already synced.
//
// Cluster-wide is the point: metav1.NamespaceAll produces a LIST and a WATCH with
// no namespace in the path, which is exactly the request Mirage confines.
func watchWidgets() chan event {
	GinkgoHelper()

	events := make(chan event, 128)
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		mirageClient(), 0, metav1.NamespaceAll, nil)
	informer := factory.ForResource(widgetGVR).Informer()

	name := func(obj any) string {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return fmt.Sprintf("<unexpected %T>", obj)
		}
		return u.GetName()
	}

	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { events <- event{"ADD", name(obj)} },
		UpdateFunc: func(_, obj any) { events <- event{"UPDATE", name(obj)} },
		DeleteFunc: func(obj any) { events <- event{"DELETE", name(obj)} },
	})
	Expect(err).NotTo(HaveOccurred())

	stop := make(chan struct{})
	DeferCleanup(func() { close(stop) })
	go informer.Run(stop)

	Expect(cache.WaitForCacheSync(stop, informer.HasSynced)).To(BeTrue(),
		"the informer never synced: its cluster-wide LIST through Mirage did not return")

	return events
}

// This is the spec ADR 0006 calls non-optional. Echo's proxy middleware streams a
// WATCH only because httputil.ReverseProxy flushes immediately when
// ContentLength is -1 — behaviour that is load-bearing and invisible in Mirage's
// own source. If a Go or Echo upgrade ever buffers instead, informers receive
// nothing, the Client starts cleanly and then silently never reconciles, and no
// error appears anywhere. This spec is the only thing that would notice.
//
// Do not delete it as redundant with the mask or confine specs. They assert on
// single responses, which buffering does not break.
var _ = Describe("A client-go informer through Mirage", Ordered, func() {
	It("receives ADD, UPDATE and DELETE from a streaming WATCH", func(ctx SpecContext) {
		events := watchWidgets()

		// Created after the cache has synced, so the events below can only have
		// arrived over the open WATCH. An object created first would show up as an
		// ADD replayed from the initial LIST, which proves nothing about streaming.
		name := uniqueName("streamed")
		created := createWidget(ctx, targetNamespace, name)

		Eventually(events, eventuallyTimeout).Should(Receive(Equal(event{"ADD", name})),
			"no ADD arrived: the WATCH response is being buffered rather than streamed, see ADR 0006")

		created.Object["spec"].(map[string]any)["note"] = "updated"
		_, err := adminDynamic.Resource(widgetGVR).Namespace(targetNamespace).
			Update(ctx, created, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Eventually(events, eventuallyTimeout).Should(Receive(Equal(event{"UPDATE", name})))

		Expect(adminDynamic.Resource(widgetGVR).Namespace(targetNamespace).
			Delete(ctx, name, metav1.DeleteOptions{})).To(Succeed())

		Eventually(events, eventuallyTimeout).Should(Receive(Equal(event{"DELETE", name})))
	})

	It("never sees objects from the foreign namespace", func(ctx SpecContext) {
		events := watchWidgets()

		foreign := uniqueName("foreign")
		createWidget(ctx, foreignNamespace, foreign)

		// Nothing to wait for, so wait for something that must arrive after it: an
		// object in the Target Namespace created second still arrives first, and
		// the foreign one having not arrived by then is the assertion.
		target := uniqueName("target")
		createWidget(ctx, targetNamespace, target)

		Eventually(events, eventuallyTimeout).Should(Receive(Equal(event{"ADD", target})))
		Consistently(events, 2*time.Second).ShouldNot(Receive(Equal(event{"ADD", foreign})))
	})
})
