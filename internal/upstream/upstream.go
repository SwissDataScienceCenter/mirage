// Package upstream locates the real Kubernetes API server and builds the
// transport Mirage uses to reach it.
//
// The transport carries no credentials. Mirage forwards whatever Authorization
// header the Client sent, so the API server remains the sole enforcement point —
// see ADR 0001.
package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
)

const (
	hostEnv = "KUBERNETES_SERVICE_HOST"
	portEnv = "KUBERNETES_SERVICE_PORT"
)

// Discover derives the Upstream URL from Mirage's own in-cluster environment.
// It is not configurable: Mirage always talks to the API server of the cluster it
// is running in.
func Discover() (*url.URL, error) {
	host, port := os.Getenv(hostEnv), os.Getenv(portEnv)
	if host == "" || port == "" {
		return nil, fmt.Errorf("cannot locate Upstream: %s and %s must both be set (is Mirage running in a Pod?)", hostEnv, portEnv)
	}
	return &url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)}, nil
}

// Transport returns the RoundTripper for talking to Upstream, verifying it
// against the cluster CA.
//
// It starts from http.DefaultTransport so that dial, handshake and idle-connection
// timeouts track the stdlib defaults; only the settings below are Mirage's own.
func Transport(pool *x509.CertPool) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()

	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	// Pinned to zero — no timeout — rather than inherited, so a future change to
	// http.DefaultTransport cannot introduce one. A WATCH sends its response
	// headers immediately but may then stay silent for minutes, and any timeout
	// here would tear down long-lived watches: the failure mode ADR 0006 is about.
	t.ResponseHeaderTimeout = 0

	// Setting TLSClientConfig disables Go's automatic HTTP/2 upgrade unless this
	// is set, and without HTTP/2 we lose request multiplexing over a single
	// connection to the API server.
	t.ForceAttemptHTTP2 = true

	// Every connection Mirage makes goes to the same host, so the default
	// per-host cap of 2 — not MaxIdleConns — is the real limit. Two idle
	// connections means constant re-dial plus TLS handshake on the paths HTTP/2
	// cannot multiplex, in particular the upgrade requests behind exec, attach
	// and port-forward.
	t.MaxIdleConnsPerHost = t.MaxIdleConns

	return t
}
