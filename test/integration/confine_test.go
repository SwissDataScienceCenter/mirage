//go:build integration

package integration_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("A Confined Resource", func() {
	Describe("a cluster-wide LIST", func() {
		It("returns only Target Namespace objects while a foreign one exists", func(ctx SpecContext) {
			mine := uniqueName("mine")
			theirs := uniqueName("theirs")
			createWidget(ctx, targetNamespace, mine)
			createWidget(ctx, foreignNamespace, theirs)

			// No namespace in the path. Mirage inserts the Target Namespace.
			list, err := mirageClient().Resource(widgetGVR).List(ctx, metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred())

			names := make([]string, 0, len(list.Items))
			namespaces := make(map[string]struct{})
			for _, item := range list.Items {
				names = append(names, item.GetName())
				namespaces[item.GetNamespace()] = struct{}{}
			}

			Expect(names).To(ContainElement(mine))
			Expect(names).NotTo(ContainElement(theirs))
			// Stronger than the two assertions above, and independent of what other
			// specs left behind: nothing outside the Target Namespace can appear.
			Expect(namespaces).To(SatisfyAny(BeEmpty(), HaveKey(targetNamespace)))
			Expect(namespaces).To(HaveLen(1))
		})
	})

	Describe("an explicit namespace in the path", func() {
		It("is left untouched, so the API server stays the one refusing it", func(ctx SpecContext) {
			theirs := uniqueName("explicit")
			createWidget(ctx, foreignNamespace, theirs)

			// The Client named the foreign namespace itself. Mirage does not rewrite
			// it — it forwards the request and the API server denies it, because the
			// test user's Role covers the Target Namespace only.
			_, err := mirageClient().Resource(widgetGVR).Namespace(foreignNamespace).
				Get(ctx, theirs, metav1.GetOptions{})
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err)).To(BeTrue(),
				"expected a 403 from the API server, got: %v", err)
		})
	})

	// Two spellings reach the same place. client-go uses ?watch=true; the legacy
	// /watch/ path prefix predates it and the API server still serves it, so
	// Mirage has to confine it too — and has to put the prefix back on the way
	// out, or the Client's watch silently becomes a one-shot list.
	Describe("both watch spellings", func() {
		It("streams for ?watch=true", func(ctx SpecContext) {
			name := uniqueName("query-watch")

			resp := through(ctx, http.MethodGet,
				"/apis/mirage.test/v1/widgets?watch=true&timeoutSeconds=30", nil)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			createWidget(ctx, targetNamespace, name)
			awaitWatched(resp.Body, name)
		})

		It("streams for the legacy /watch/ path prefix", func(ctx SpecContext) {
			name := uniqueName("path-watch")

			resp := through(ctx, http.MethodGet,
				"/apis/mirage.test/v1/watch/widgets?timeoutSeconds=30", nil)
			// If a future API server drops the legacy form this fails here rather
			// than mysteriously downstream — which is itself worth knowing.
			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"the API server no longer serves the legacy /watch/ prefix")

			createWidget(ctx, targetNamespace, name)
			awaitWatched(resp.Body, name)
		})
	})
})

// awaitWatched reads watch events off an open stream until one names want, and
// fails if the stream ends first.
//
// It scans rather than reading a single event because a watch opened without a
// resourceVersion replays the objects already in the collection, and other specs
// leave some behind — envtest garbage-collects nothing.
//
// Arriving at all is the assertion: the object is created after the stream is
// open, so a buffering proxy would deliver nothing until the API server closed
// the watch at timeoutSeconds, by which point the scan has run out of stream.
func awaitWatched(body io.Reader, want string) {
	GinkgoHelper()

	var ev struct {
		Type   string `json:"type"`
		Object struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"object"`
	}

	// One JSON event per line, and a Widget is small, but the default 64 KiB token
	// limit is one surprise away from turning a pass into a confusing failure.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Object.Metadata.Name == want {
			return
		}
	}
	Expect(scanner.Err()).NotTo(HaveOccurred())
	Fail("the watch stream ended without ever delivering " + want +
		"; the response is being buffered rather than streamed, see ADR 0006")
}
