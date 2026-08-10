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
	"time"
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
// ResponseHeaderTimeout is deliberately left unset. A WATCH sends its response
// headers immediately but may then stay silent for minutes, and any timeout here
// would tear down long-lived watches — the failure mode ADR 0006 is about.
func Transport(pool *x509.CertPool) http.RoundTripper {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}
