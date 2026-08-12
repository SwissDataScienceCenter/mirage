// Command mirage runs the proxy.
//
// It is a sidecar in the Client's Pod. It listens on loopback over HTTPS with a
// self-signed certificate (ADR 0002), presents its own namespace as the whole
// cluster (ADR 0003), and forwards the Client's credentials without ever holding
// any of its own (ADR 0001).
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/selfsign"
	"github.com/SwissDataScienceCenter/mirage/internal/server"
	"github.com/SwissDataScienceCenter/mirage/internal/serviceaccount"
	"github.com/SwissDataScienceCenter/mirage/internal/upstream"
)

// shutdownGrace bounds how long Mirage waits for in-flight requests when the
// kubelet stops it. As a native sidecar it is stopped only after the Client has
// exited, so there should be little in flight — but the Client's last calls
// include releasing its leader-election Lease, and cutting those off is visible
// in the next leader's startup delay.
const shutdownGrace = 15 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("mirage exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "/etc/mirage/config.yaml", "path to Mirage's configuration file")
		listen     = flag.String("listen", "127.0.0.1:8001", "address to listen on; loopback only, HTTPS, see ADR 0002")
		logLevel   = flag.String("log-level", "info", "one of debug, info, warn, error")
	)
	flag.Parse()

	level, err := parseLevel(*logLevel)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	sa := serviceaccount.Dir(serviceaccount.DefaultDir)
	namespace, err := sa.Namespace()
	if err != nil {
		return err
	}
	pool, err := sa.CertPool()
	if err != nil {
		return err
	}
	upstreamURL, err := upstream.Discover()
	if err != nil {
		return err
	}

	// Generated per start and held only in memory. It authenticates nothing — the
	// Client is configured to skip verification — but without it the listener is
	// plaintext, and clientcmd drops the Client's credentials rather than send
	// them to an http:// server. See ADR 0002.
	certPEM, keyPEM, err := selfsign.Certificate()
	if err != nil {
		return err
	}

	// Echo the whole resolved configuration back, so the first lines of Mirage's
	// logs say what it actually loaded rather than what was intended.
	log.Info("mirage starting",
		// With the scheme, because it is the half of the address the Client's
		// kubeconfig gets wrong: an http:// server there costs it its credentials
		// silently. See ADR 0002.
		slog.String("listen", "https://"+*listen),
		slog.String("targetNamespace", namespace),
		slog.String("upstream", upstreamURL.String()),
		slog.Any("config", cfg),
	)

	e, err := server.New(server.Options{
		Config:          cfg,
		TargetNamespace: namespace,
		Upstream:        upstreamURL,
		Transport:       upstream.Transport(pool),
		Logger:          log,
	})
	if err != nil {
		return err
	}

	// Echo v5 has no Echo.Shutdown; the server lifecycle is a StartConfig driven
	// by a context. Start blocks until the context is cancelled and then shuts
	// down gracefully, so cancelling on SIGTERM is the whole of it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	start := echo.StartConfig{
		Address:         *listen,
		GracefulTimeout: shutdownGrace,
		// The certificate is supplied to StartTLS below; this is only the floor on
		// the negotiated version, set here rather than inherited so it cannot drift
		// with the stdlib default.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		// Mirage logs its own startup line, with rather more in it.
		HideBanner: true,
		HidePort:   true,
		OnShutdownError: func(err error) {
			log.Error("shutdown did not complete in time", slog.Any("error", err))
		},
		BeforeServeFunc: func(s *http.Server) error {
			// Echo defaults ReadTimeout to 30s to bound slow request headers. That
			// bound belongs on the headers alone: a WATCH is a request that stays
			// open for minutes, and a whole-request timeout would cut it off —
			// silently, since informers just stop receiving. See ADR 0006.
			s.ReadTimeout = 0
			s.ReadHeaderTimeout = 30 * time.Second
			// WriteTimeout stays 0 for the same reason: the response side of a
			// WATCH is long-lived by design.
			s.WriteTimeout = 0
			return nil
		},
	}

	if err := start.StartTLS(ctx, e, certPEM, keyPEM); err != nil {
		return err
	}
	log.Info("mirage stopped")
	return nil
}

func parseLevel(name string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("invalid --log-level %q: want debug, info, warn or error", name)
	}
	return level, nil
}
