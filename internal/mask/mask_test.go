package mask_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/labstack/echo/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/decide"
	"github.com/SwissDataScienceCenter/mirage/internal/mask"
)

const (
	collectionPath = "/apis/shipwright.io/v1beta1/clusterbuildstrategies"
	namedPath      = collectionPath + "/buildah"
)

var _ = Describe("Handler", func() {
	var (
		handler *mask.Handler
		decider *decide.Decider
		e       *echo.Echo
	)

	BeforeEach(func() {
		handler = mask.New(slog.New(slog.DiscardHandler))
		decider = decide.New(config.Config{
			Masked: []config.Masked{
				{
					Resource: config.Resource{Group: "shipwright.io", Resource: "clusterbuildstrategies"},
					Kind:     "ClusterBuildStrategy",
				},
			},
		}, "tenant-a")
		e = echo.New()
	})

	// serve runs one request through the Handler and returns what it wrote.
	serve := func(method, target string, header http.Header) *httptest.ResponseRecorder {
		GinkgoHelper()

		req := httptest.NewRequest(method, target, nil)
		for k, values := range header {
			req.Header[k] = values
		}
		rec := httptest.NewRecorder()

		d := decider.Decide(method, req.URL.Path)
		Expect(d.Action).To(Equal(decide.Mask), "the fixture should be a Masked Resource")
		Expect(handler.Handle(e.NewContext(req, rec), d)).To(Succeed())

		return rec
	}

	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		GinkgoHelper()

		var body map[string]any
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		return body
	}

	Describe("LIST", func() {
		It("answers an empty list named after the configured Kind", func() {
			rec := serve(http.MethodGet, collectionPath, nil)

			Expect(rec.Code).To(Equal(http.StatusOK))
			body := decode(rec)
			Expect(body["kind"]).To(Equal("ClusterBuildStrategyList"))
			Expect(body["apiVersion"]).To(Equal("shipwright.io/v1beta1"))
			Expect(body["items"]).To(BeEmpty())
		})

		It("reports a constant resourceVersion", func() {
			// A masked collection never changes, so a Client that LISTs at this
			// version and then WATCHes from it misses nothing.
			body := decode(serve(http.MethodGet, collectionPath, nil))
			Expect(body["metadata"]).To(HaveKeyWithValue("resourceVersion", "1"))
		})
	})

	Describe("GET of a named object", func() {
		It("answers 404, because the resource exists but holds nothing", func() {
			rec := serve(http.MethodGet, namedPath, nil)

			Expect(rec.Code).To(Equal(http.StatusNotFound))
			body := decode(rec)
			Expect(body["kind"]).To(Equal("Status"))
			Expect(body["reason"]).To(Equal("NotFound"))
			Expect(body["message"]).To(ContainSubstring("buildah"))
		})
	})

	DescribeTable("mutations",
		func(method string) {
			rec := serve(method, namedPath, nil)

			Expect(rec.Code).To(Equal(http.StatusForbidden))
			// client-go decodes a well-formed Status into a typed error; a bare
			// string body yields an uninformative failure in the Client's logs.
			body := decode(rec)
			Expect(body["kind"]).To(Equal("Status"))
			Expect(body["status"]).To(Equal("Failure"))
			Expect(body["reason"]).To(Equal("Forbidden"))
			Expect(body["code"]).To(BeEquivalentTo(http.StatusForbidden))
		},
		Entry("POST", http.MethodPost),
		Entry("PUT", http.MethodPut),
		Entry("PATCH", http.MethodPatch),
		Entry("DELETE", http.MethodDelete),
	)

	Describe("content negotiation", func() {
		It("refuses a Client that will accept nothing but protobuf", func() {
			// Exactly what a real API server does for a custom resource.
			rec := serve(http.MethodGet, collectionPath, http.Header{
				"Accept": {"application/vnd.kubernetes.protobuf"},
			})

			Expect(rec.Code).To(Equal(http.StatusNotAcceptable))
			Expect(decode(rec)["reason"]).To(Equal("NotAcceptable"))
		})

		It("answers in JSON when the Client offers it as an alternative", func() {
			rec := serve(http.MethodGet, collectionPath, http.Header{
				"Accept": {"application/vnd.kubernetes.protobuf, application/json"},
			})

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(decode(rec)["kind"]).To(Equal("ClusterBuildStrategyList"))
		})
	})

	Describe("WATCH", func() {
		It("closes cleanly at timeoutSeconds instead of holding the connection open", func() {
			// A reflector picks a randomised 5–10 minute timeout and expects the
			// server to close so it can re-LIST. Holding forever puts it in a
			// state the real API server never produces.
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				defer GinkgoRecover()
				done <- serve(http.MethodGet, collectionPath+"?watch=true&timeoutSeconds=1", nil)
			}()

			var rec *httptest.ResponseRecorder
			Eventually(done, "5s").Should(Receive(&rec))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Header().Get(echo.HeaderContentType)).To(ContainSubstring(echo.MIMEApplicationJSON))
			// No events, ever.
			Expect(io.ReadAll(rec.Body)).To(BeEmpty())
		})

		It("recognises the legacy /watch/ path prefix, not only ?watch=true", func() {
			// Same request spelled the older way. Answering it as a LIST would put a
			// JSON list on a connection the Client is reading as an event stream.
			legacy := "/apis/shipwright.io/v1beta1/watch/clusterbuildstrategies?timeoutSeconds=1"

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				defer GinkgoRecover()
				done <- serve(http.MethodGet, legacy, nil)
			}()

			var rec *httptest.ResponseRecorder
			Eventually(done, "5s").Should(Receive(&rec))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(io.ReadAll(rec.Body)).To(BeEmpty())
		})
	})
})
