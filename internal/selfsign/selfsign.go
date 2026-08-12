// Package selfsign generates the certificate Mirage serves on loopback.
//
// The certificate exists to make the listener HTTPS, not to prove anything.
// clientcmd refuses to read a kubeconfig's credentials when the server URL is
// not `https://` — see ADR 0002 — so a plaintext listener silently costs Mirage
// the Client's bearer token, which is the one thing it cannot work without. The
// Client is told to skip verification, so nothing ever checks this certificate;
// it only has to exist.
//
// It is generated fresh on every start and never leaves the process. There is no
// CA to distribute and nothing to rotate.
package selfsign

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

const (
	// validity is long because nothing renews this certificate: it lives as long
	// as the process, and a Pod can run for months. Since the Client skips
	// verification an expiry would not even be noticed by it — but it would be by
	// anyone who later decides to pin the certificate properly, and an expired
	// cert failing at that point is a worse discovery than a long-lived one.
	validity = 10 * 365 * 24 * time.Hour

	// backdate covers clock skew between Mirage and whatever might one day check
	// NotBefore.
	backdate = time.Hour
)

// Certificate returns a fresh self-signed certificate and its private key, PEM
// encoded, valid for the loopback addresses Mirage listens on.
func Certificate() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate loopback key: %w", err)
	}

	// 128 bits, per CA/Browser Forum practice. Nothing depends on it here, but a
	// serial of 1 is the kind of detail a scanner flags and someone then has to
	// spend an afternoon explaining.
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate loopback certificate serial: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mirage"},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Both loopback addresses and the name, so the certificate stays correct
		// whichever of them the Client's kubeconfig happens to name.
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create loopback certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal loopback key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		nil
}
