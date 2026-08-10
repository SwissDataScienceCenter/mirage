// Package mask answers requests for Masked Resources without contacting Upstream.
//
// A Masked Resource always exists and is always empty. Where a choice existed we
// imitate the real API server rather than doing the simplest thing, because the
// Client is client-go and client-go notices the difference — see ADR 0004.
//
// The JSON shapes below are hand-written rather than imported from apimachinery.
// There are four of them, they are part of the API's stable wire contract, and
// keeping them local keeps Mirage's dependency list to Echo and a YAML parser.
package mask

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/SwissDataScienceCenter/mirage/internal/decide"
)

// resourceVersion is what Mirage reports for every Masked Resource. A masked
// collection never changes, so a constant is honest: a Client that LISTs at 1 and
// then WATCHes from 1 misses nothing.
const resourceVersion = "1"

// bookmarkInterval is how often a WATCH emits a Bookmark when the Client asked for
// them, roughly matching the real API server's cadence.
const bookmarkInterval = time.Minute

// protobufMediaType is what client-go negotiates for built-in types. A real API
// server does not speak it for custom resources, and neither do we.
const protobufMediaType = "application/vnd.kubernetes.protobuf"

// Handler answers requests that Decide classified as Mask.
type Handler struct {
	log *slog.Logger
}

// New builds a Handler.
func New(log *slog.Logger) *Handler {
	return &Handler{log: log}
}

// Handle answers one request for a Masked Resource. Upstream is never contacted.
func (h *Handler) Handle(c *echo.Context, d decide.Decision) error {
	req := c.Request()

	if demandsProtobuf(req.Header.Get(echo.HeaderAccept)) {
		// Exactly what a real API server does for a custom resource, so a
		// well-behaved client retries in JSON.
		return h.status(c, http.StatusNotAcceptable, "NotAcceptable",
			"only the following media types are accepted: application/json", d)
	}

	switch req.Method {
	case http.MethodGet:
		switch {
		case !d.Target.Collection():
			// The resource exists but holds nothing, so any named object is absent.
			return h.status(c, http.StatusNotFound, "NotFound",
				qualified(d)+` "`+d.Target.Name+`" not found`, d)
		// Either spelling: ?watch=true, or the legacy /watch/ path prefix the
		// Target carries. Answering a legacy-path watch with a list would hand the
		// Client a body it is not parsing for.
		case d.Target.Watch || isWatch(req.URL.Query().Get("watch")):
			return h.watch(c, d)
		default:
			return c.JSON(http.StatusOK, emptyList(d))
		}

	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return h.status(c, http.StatusForbidden, "Forbidden",
			qualified(d)+" is masked by Mirage and is read-only", d)

	default:
		return h.status(c, http.StatusMethodNotAllowed, "MethodNotAllowed",
			"the method "+req.Method+" is not supported on "+qualified(d), d)
	}
}

// watch answers a WATCH with a chunked 200 that emits no events.
//
// It closes at timeoutSeconds rather than holding the connection open forever.
// Holding it open is less code, but it puts a client-go reflector into a state the
// real API server never produces: the reflector picks a randomised 5–10 minute
// timeout and expects the server to close so it can re-LIST.
func (h *Handler) watch(c *echo.Context, d decide.Decision) error {
	req := c.Request()
	res := c.Response()

	res.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	res.WriteHeader(http.StatusOK)

	// Echo v5's Context.Response() is typed as http.ResponseWriter, so reach the
	// flusher through a ResponseController rather than asserting the concrete type.
	stream := http.NewResponseController(res)
	// Flush the headers so the Client knows the watch is established. Without
	// this the response sits in a buffer and the Client waits for a stream it has
	// no reason to believe started.
	if err := stream.Flush(); err != nil {
		return err
	}

	// Events are written straight to the stream as newline-delimited JSON.
	// c.JSON would try to set the status a second time on an already-committed
	// response.
	events := json.NewEncoder(res)

	done := req.Context().Done()

	var expiry <-chan time.Time
	if seconds, err := strconv.Atoi(req.URL.Query().Get("timeoutSeconds")); err == nil && seconds > 0 {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		expiry = timer.C
	}

	var bookmarks <-chan time.Time
	if isWatch(req.URL.Query().Get("allowWatchBookmarks")) {
		ticker := time.NewTicker(bookmarkInterval)
		defer ticker.Stop()
		bookmarks = ticker.C
	}

	for {
		select {
		case <-done:
			return nil
		case <-expiry:
			// A clean close, which is what the reflector is waiting for.
			return nil
		case <-bookmarks:
			if err := events.Encode(bookmarkEvent(d)); err != nil {
				return err
			}
			if err := stream.Flush(); err != nil {
				return err
			}
		}
	}
}

func (h *Handler) status(c *echo.Context, code int, reason, message string, d decide.Decision) error {
	h.log.Debug("masked request refused",
		slog.String("path", c.Request().URL.Path),
		slog.Int("code", code),
		slog.String("reason", reason),
	)
	// client-go decodes a Status into a typed error. A bare string body would
	// surface in the Client's logs as an uninformative failure.
	return c.JSON(code, status{
		Kind:       "Status",
		APIVersion: "v1",
		Metadata:   struct{}{},
		Status:     "Failure",
		Message:    message,
		Reason:     reason,
		Details: &statusDetails{
			Name:  d.Target.Name,
			Group: d.Target.Group,
			Kind:  d.Target.Resource,
		},
		Code: code,
	})
}

// demandsProtobuf reports whether the Client will accept nothing but protobuf.
// client-go usually offers JSON as an alternative, in which case we answer in JSON
// rather than refusing.
func demandsProtobuf(accept string) bool {
	if !strings.Contains(accept, protobufMediaType) {
		return false
	}
	for part := range strings.SplitSeq(accept, ",") {
		media, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if media == echo.MIMEApplicationJSON || media == "*/*" || media == "application/*" {
			return false
		}
	}
	return true
}

// isWatch interprets a Kubernetes boolean query parameter. A bare `?watch` with no
// value counts as true, as it does at the API server.
func isWatch(value string) bool {
	if value == "" {
		return false
	}
	b, err := strconv.ParseBool(value)
	return err == nil && b
}

// qualified renders a resource the way the API server names it in error messages,
// e.g. "clusterbuildstrategies.shipwright.io".
func qualified(d decide.Decision) string {
	if d.Target.Group == "" {
		return d.Target.Resource
	}
	return d.Target.Resource + "." + d.Target.Group
}

func apiVersion(d decide.Decision) string {
	if d.Target.Group == "" {
		return d.Target.Version
	}
	return d.Target.Group + "/" + d.Target.Version
}

func emptyList(d decide.Decision) list {
	return list{
		APIVersion: apiVersion(d),
		Kind:       d.Masked.Kind + "List",
		Metadata:   listMeta{ResourceVersion: resourceVersion},
		Items:      []struct{}{},
	}
}

func bookmarkEvent(d decide.Decision) watchEvent {
	return watchEvent{
		Type: "BOOKMARK",
		Object: object{
			APIVersion: apiVersion(d),
			Kind:       d.Masked.Kind,
			Metadata:   listMeta{ResourceVersion: resourceVersion},
		},
	}
}

type listMeta struct {
	ResourceVersion string `json:"resourceVersion"`
}

type list struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   listMeta   `json:"metadata"`
	Items      []struct{} `json:"items"`
}

type object struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   listMeta `json:"metadata"`
}

type watchEvent struct {
	Type   string `json:"type"`
	Object object `json:"object"`
}

type statusDetails struct {
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

type status struct {
	Kind       string         `json:"kind"`
	APIVersion string         `json:"apiVersion"`
	Metadata   struct{}       `json:"metadata"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Reason     string         `json:"reason"`
	Details    *statusDetails `json:"details,omitempty"`
	Code       int            `json:"code"`
}
