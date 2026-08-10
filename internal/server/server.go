// Package server wires Mirage's HTTP surface: a /healthz route, and a catch-all
// carrying the decide middleware followed by Echo's proxy middleware.
//
// See ADR 0006 for why the proxy middleware is used as-is rather than building a
// httputil.ReverseProxy by hand, and for the streaming behaviour that makes WATCH
// work without configuration.
package server

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/decide"
	"github.com/SwissDataScienceCenter/mirage/internal/mask"
)

// Options is everything Mirage resolved at startup.
type Options struct {
	Config config.Config
	// TargetNamespace is the namespace Mirage presents as the whole cluster.
	TargetNamespace string
	// Upstream is the real API server.
	Upstream *url.URL
	// Transport reaches Upstream. It carries no credentials of its own — the
	// Client's Authorization header is what authenticates the request.
	Transport http.RoundTripper
	Logger    *slog.Logger
}

// New builds the Echo server.
func New(o Options) (*echo.Echo, error) {
	e := echo.New()
	e.Logger = o.Logger

	e.GET("/healthz", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	proxy, err := middleware.ProxyConfig{
		// A single target, so the balancer choice is immaterial.
		Balancer: middleware.NewRoundRobinBalancer([]*middleware.ProxyTarget{
			{Name: "upstream", URL: o.Upstream},
		}),
		Transport: o.Transport,
		// Rewrite and RegexRewrite are deliberately empty. They are static
		// patterns and cannot express Mirage's rule, which depends on the
		// configuration and on whether the path already names a namespace. The
		// decide middleware does the rewriting instead.
	}.ToMiddleware()
	if err != nil {
		return nil, err
	}

	deciding := Deciding(decide.New(o.Config, o.TargetNamespace), mask.New(o.Logger), o.Logger)

	// Everything that is not /healthz. The handler is never reached: the proxy
	// middleware terminates the chain by writing the Upstream response itself.
	e.Any("/*", unreachable, deciding, proxy)

	return e, nil
}

// Deciding classifies each request and acts on it: it answers Masked Resources
// itself, rewrites the path for Handled Resources, and otherwise leaves the
// request exactly as it arrived.
func Deciding(d *decide.Decider, m *mask.Handler, log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			inbound := req.URL.Path
			dec := d.Decide(req.Method, inbound)

			if dec.Warn {
				// The signature of a resource missing from `handled`: a
				// cluster-wide collection request Mirage was not told about, on
				// its way to a 403 the Client will not explain.
				log.Warn("cluster-wide request passed through unchanged; if the Client is getting 403s, this resource may be missing from `handled`",
					slog.String("method", req.Method),
					slog.String("path", inbound),
				)
			}

			log.Debug("decided",
				slog.String("decision", string(dec.Action)),
				slog.String("method", req.Method),
				slog.String("inbound", inbound),
				slog.String("outbound", dec.Path),
			)

			switch dec.Action {
			case decide.Mask:
				return m.Handle(c, dec)
			case decide.Rewrite:
				req.URL.Path = dec.Path
				// Clear RawPath so EscapedPath() re-derives the encoding from the
				// new Path rather than serving the stale original.
				req.URL.RawPath = ""
			default:
				// Do nothing - i.e. pass through.
			}

			// The inbound Host is Mirage's own loopback address, which means
			// nothing to Upstream. Clearing it makes the transport use the
			// Upstream host instead.
			req.Host = ""

			return next(c)
		}
	}
}

func unreachable(c *echo.Context) error {
	return echo.NewHTTPError(http.StatusBadGateway, "request was not proxied")
}
