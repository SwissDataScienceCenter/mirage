package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/labstack/echo/v5"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/server"
)

// These specs guard the streaming dependency described in ADR 0006: a WATCH is a
// single response that stays open, so every byte Upstream flushes must reach the
// Client immediately. If the response is buffered anywhere in the chain the
// failure is silent — informers receive nothing and the controller never
// reconciles — so it is checked here rather than assumed.
//
// Neither spec needs a real API server. What is under test is Echo's plumbing,
// not Kubernetes semantics; the envtest tier tracked in TODO.md covers the rest.
var _ = Describe("Streaming", func() {
	// watchPath is Passed Through under an empty config, so the request reaches
	// the proxy middleware exactly as it arrived.
	const watchPath = "/api/v1/namespaces/tenant-a/pods?watch=true"

	It("hands the handler a ResponseWriter that implements http.Flusher", func() {
		// httputil.ReverseProxy, which Echo's proxy middleware builds on, streams
		// a response only if the ResponseWriter it is given is flushable —
		// otherwise it writes the body out in one piece at the end. The
		// ResponseWriter it is given is Echo's own. This held in Echo v4; v5
		// changed echo.Context from an interface to a struct, so it is confirmed
		// rather than inherited.
		var flushable bool

		e := echo.New()
		e.GET("/", func(c *echo.Context) error {
			_, flushable = any(c.Response()).(http.Flusher)
			return c.String(http.StatusOK, "ok")
		})

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(flushable).To(BeTrue(),
			"echo's ResponseWriter no longer implements http.Flusher; WATCH responses will be buffered until Upstream closes them and informers will receive nothing")
	})

	It("delivers a flushed chunk before Upstream has finished responding", func(ctx SpecContext) {
		// The end-to-end version of the same property, through the real middleware
		// chain. Upstream flushes one chunk and then stalls; if the Client can read
		// that chunk while Upstream is still stalled, nothing in between buffered.
		released := make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		// Registered before the server so the handler is never left blocked on a
		// failure path, which would hang Close.
		DeferCleanup(release)

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "first\n")
			w.(http.Flusher).Flush()

			select {
			case <-released:
			case <-r.Context().Done():
			}

			_, _ = io.WriteString(w, "second\n")
		}))
		DeferCleanup(upstream.Close)

		upstreamURL, err := url.Parse(upstream.URL)
		Expect(err).NotTo(HaveOccurred())

		e, err := server.New(server.Options{
			Config:          config.Config{},
			TargetNamespace: "tenant-a",
			Upstream:        upstreamURL,
			Transport:       http.DefaultTransport,
			Logger:          slog.New(slog.DiscardHandler),
		})
		Expect(err).NotTo(HaveOccurred())

		mirage := httptest.NewServer(e)
		DeferCleanup(mirage.Close)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mirage.URL+watchPath, nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = resp.Body.Close() })
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// Blocks until the first chunk arrives. Reaching the assertion at all is
		// the result: Upstream is still inside its handler, so the bytes were not
		// held back until the response completed. A buffering regression hangs
		// here and the spec timeout reports it.
		first := make([]byte, len("first\n"))
		_, err = io.ReadFull(resp.Body, first)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(first)).To(Equal("first\n"))

		release()

		rest, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rest)).To(Equal("second\n"))
	}, SpecTimeout(30*time.Second))
})

var _ = Describe("Healthz", func() {
	It("answers without touching Upstream", func() {
		// Upstream is deliberately a URL nothing is listening on: if /healthz were
		// proxied this would fail rather than quietly pass.
		e, err := server.New(server.Options{
			Config:          config.Config{},
			TargetNamespace: "tenant-a",
			Upstream:        &url.URL{Scheme: "http", Host: "127.0.0.1:1"},
			Transport:       http.DefaultTransport,
			Logger:          slog.New(slog.DiscardHandler),
		})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
	})
})
