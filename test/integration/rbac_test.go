//go:build integration

package integration_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Mirage's whole reason for existing, proven rather than asserted.
//
// The test user is bound to a Role in the Target Namespace and nothing else — no
// ClusterRole, no ClusterRoleBinding. A cluster-wide LIST is therefore a 403 at
// the API server. The same LIST through Mirage succeeds, because Mirage turned it
// into a namespaced one the user is allowed to make.
//
// Nothing here relaxes enforcement: the API server evaluates the same identity in
// both cases and reaches opposite answers because the requests are different. See
// ADR 0001.
var _ = Describe("RBAC end to end", func() {
	It("turns a forbidden cluster-wide LIST into an allowed namespaced one", func(ctx SpecContext) {
		name := uniqueName("rbac")
		createWidget(ctx, targetNamespace, name)

		direct, err := dynamic.NewForConfig(directCfg)
		Expect(err).NotTo(HaveOccurred())

		_, err = direct.Resource(widgetGVR).List(ctx, metav1.ListOptions{})
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsForbidden(err)).To(BeTrue(),
			"the cluster-wide LIST was expected to be forbidden straight to the API server; "+
				"if it succeeded the suite is running under AlwaysAllow and proves nothing: %v", err)

		list, err := mirageClient().Resource(widgetGVR).List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred(),
			"the same LIST through Mirage should have been confined to %s and allowed", targetNamespace)

		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, item.GetName())
		}
		Expect(names).To(ContainElement(name))
	})

	It("forwards the Client's credentials rather than substituting its own", func(ctx SpecContext) {
		// Mirage holds no credentials, so a request arriving without an
		// Authorization header must reach the API server anonymous. Anything other
		// than a rejection here means Mirage acquired authority of its own, which
		// ADR 0001 forbids.
		anonymous, err := dynamic.NewForConfig(&rest.Config{Host: mirage.URL})
		Expect(err).NotTo(HaveOccurred())

		_, err = anonymous.Resource(widgetGVR).List(ctx, metav1.ListOptions{})
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err)).To(BeTrue(),
			"an unauthenticated request through Mirage was answered; Mirage is adding authority: %v", err)
	})
})
