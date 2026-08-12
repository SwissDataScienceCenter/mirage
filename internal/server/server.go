// Package server wires Mirage's HTTP surface: logging and panic recovery over
// everything, a /healthz route, and a catch-all carrying the decide middleware
// followed by Echo's proxy middleware.
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

	// Global rather than route-level, so both also cover the responses Echo
	// generates before routing — a 404 above all. Route-level middleware, which is
	// what the deciding middleware below is, never runs for those.
	//
	// Logging wraps Recover so that a recovered panic reaches it as a value to log:
	// Echo's Recover does not log anything itself, it converts the panic into a
	// PanicStackError and returns it up the chain.
	e.Use(Logging(o.Logger), middleware.Recover())

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
		// deciding middleware edits the path instead.

		// Without this, a failure on the leg between Mirage and Upstream is
		// invisible from both ends. Echo's proxy middleware installs its own
		// ErrorHandler on the underlying httputil.ReverseProxy, which suppresses
		// the stdlib's "http: proxy error" line, and then answers 502 with a JSON
		// body that is not a metav1.Status — and client-go renders any non-Status,
		// non-text error body as the single word "unknown". So the Client reports
		// nothing usable and Mirage reports nothing at all.
		ErrorHandler: func(c *echo.Context, err error) error {
			o.Logger.Error("could not forward to upstream",
				slog.String("method", c.Request().Method),
				// Post-confinement, so this is the path Mirage actually asked
				// Upstream for — the same value the decided line calls outbound.
				slog.String("outbound", c.Request().URL.Path),
				slog.String("upstream", o.Upstream.String()),
				slog.Any("error", err),
			)
			// Returned unchanged: the Client sees exactly the response it saw
			// before. This handler only makes the failure observable.
			return err
		},
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

// Logging emits one line per request once the response is complete.
//
// It overlaps with the decided line the deciding middleware writes, deliberately:
// that one says what Mirage set out to do with a request, this one says what came
// of it. The status is the part neither the decided line nor the Client can
// supply, and its absence is what makes a proxy failure hard to place — the
// Client is told only "unknown".
//
// A WATCH is logged when it ends rather than when it starts, since that is when
// the status and latency exist. A long-lived watch therefore shows up minutes
// after the decided line that opened it, or not at all until shutdown.
func Logging(log *slog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:  true,
		LogURIPath: true,
		LogStatus:  true,
		LogLatency: true,
		// Let Echo's error handler run before the values are read, so Status is
		// the status the Client received rather than the zero value a handler
		// that returned an error would otherwise leave behind.
		HandleError: true,
		LogValuesFunc: func(_ *echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []any{
				slog.String("method", v.Method),
				slog.String("path", v.URIPath),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
			}
			if v.Error != nil {
				// Carries the recovered panic and its stack when Recover handled
				// one, which is the only place either is recorded.
				attrs = append(attrs, slog.Any("error", v.Error))
			}

			// A 5xx is Mirage's own failure — it forwards Upstream's own statuses
			// untouched, so anything in that range was generated here. Loud by
			// default, at the level Mirage runs at in production. Everything else,
			// including the 4xxs that are Upstream enforcing RBAC exactly as ADR
			// 0001 intends, is ordinary traffic.
			if v.Status >= http.StatusInternalServerError || v.Error != nil {
				log.Error("request failed", attrs...)
				return nil //nolint:nilerr
			}
			log.Debug("request", attrs...)
			return nil
		},
	})
}

// Deciding classifies each request and acts on it: it answers Masked Resources
// itself, confines Confined Resources to the Target Namespace, and otherwise
// leaves the request exactly as it arrived.
func Deciding(d *decide.Decider, m *mask.Handler, log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			inbound := req.URL.Path
			dec := d.Decide(req.Method, inbound)

			if dec.Warn {
				// The signature of a resource missing from `confined`: a
				// cluster-wide collection request Mirage was not told about, on
				// its way to a 403 the Client will not explain.
				log.Warn("cluster-wide request passed through unchanged; if the Client is getting 403s, this resource may be missing from `confined`",
					slog.String("method", req.Method),
					slog.String("path", inbound),
				)
			}

			log.Debug("decided",
				slog.String("decision", string(dec.Action)),
				slog.String("method", req.Method),
				slog.String("inbound", inbound),
				slog.String("outbound", dec.Path),
				// Whether the Client authenticated itself, never what with. Mirage
				// holds no credentials and forwards this header untouched (ADR
				// 0001), so if it is absent here the request will reach Upstream as
				// system:anonymous and come back 403 — a failure that otherwise
				// looks identical to a missing `confined` entry.
				slog.Bool("authorization", req.Header.Get("Authorization") != ""),
			)

			switch dec.Action {
			case decide.Mask:
				return m.Handle(c, dec)
			case decide.Confine:
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
