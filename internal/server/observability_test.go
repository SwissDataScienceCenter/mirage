package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/server"
)

// These specs guard the one thing Mirage cannot learn from the Client: why a
// request failed on the leg between Mirage and Upstream. Echo answers such a
// failure with a 502 whose JSON body is not a metav1.Status, and client-go
// collapses any such body to the single word "unknown" — so if Mirage does not
// record the cause, nothing does.
var _ = Describe("Observability", func() {
	// unreachable is a loopback port nothing is listening on, so every proxied
	// request fails at dial and takes the ErrorHandler path.
	var unreachable = &url.URL{Scheme: "http", Host: "127.0.0.1:1"}

	// passedThrough is Passed Through under an empty config, so it reaches the
	// proxy middleware rather than being answered by Mirage.
	const passedThrough = "/api/v1/namespaces/tenant-a/pods"

	It("logs the cause when it cannot reach Upstream", func() {
		records := &recorder{}

		e, err := server.New(server.Options{
			Config:          config.Config{},
			TargetNamespace: "tenant-a",
			Upstream:        unreachable,
			Transport:       http.DefaultTransport,
			Logger:          slog.New(records),
		})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, passedThrough, nil))

		// The response is Echo's, unchanged — the ErrorHandler only observes.
		Expect(rec.Code).To(Equal(http.StatusBadGateway))

		errors := records.at(slog.LevelError)
		Expect(errors).NotTo(BeEmpty(),
			"a request that never reached Upstream was not logged; the Client sees only client-go's \"unknown\" and Mirage has recorded nothing")
		Expect(errors).To(ContainElement(ContainSubstring(unreachable.String())),
			"the failure was logged without naming Upstream")
		Expect(errors).To(ContainElement(ContainSubstring(passedThrough)),
			"the failure was logged without naming the request it belongs to")
	})

	It("logs a request the router rejected, which no route-level middleware would see", func() {
		// Echo answers an unroutable request from the router itself, outside the
		// route's own middleware — so the deciding middleware never sees it, and
		// before this was global middleware neither did anything else. The body is
		// JSON and not a Status, which the Client also reports as "unknown",
		// making it the same class of silent failure as the spec above.
		records := &recorder{}

		e, err := server.New(server.Options{
			Config:          config.Config{},
			TargetNamespace: "tenant-a",
			Upstream:        unreachable,
			Transport:       http.DefaultTransport,
			Logger:          slog.New(records),
		})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		// POST to the health route: the path is registered, the method is not.
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))

		Expect(rec.Code).To(BeNumerically(">=", http.StatusBadRequest))
		Expect(records.all()).NotTo(BeEmpty(),
			"a request Echo rejected before routing was not logged at all")
	})
})

// recorder is a slog.Handler that keeps every record it is given, formatted as
// one string per record with its attributes appended, which is all the specs
// above need to assert on.
type recorder struct {
	mu      sync.Mutex
	entries []entry
}

type entry struct {
	level slog.Level
	text  string
}

func (r *recorder) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	text := rec.Message
	rec.Attrs(func(a slog.Attr) bool {
		text += " " + a.String()
		return true
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry{level: rec.Level, text: text})
	return nil
}

func (r *recorder) WithAttrs(_ []slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(_ string) slog.Handler      { return r }

// at returns the text of every record logged at the given level.
func (r *recorder) at(level slog.Level) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string
	for _, e := range r.entries {
		if e.level == level {
			out = append(out, e.text)
		}
	}
	return out
}

// all returns the text of every record, at any level.
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.text)
	}
	return out
}
