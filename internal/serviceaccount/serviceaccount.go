// Package serviceaccount reads the projected ServiceAccount volume that the
// kubelet mounts into every Pod.
//
// Mirage reads two things from it: the namespace, which is the Target Namespace,
// and the cluster CA, which it uses to reach Upstream. It deliberately never
// reads the token — see ADR 0001. Mirage forwards the Client's own Authorization
// header and holds no credentials of its own.
package serviceaccount

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir is where the kubelet projects the ServiceAccount volume. Removing or
// remapping this mount breaks Mirage, the Client's kubeconfig and controller-
// runtime's leader election, none of which warn you.
const DefaultDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// Dir is a projected ServiceAccount volume on disk.
type Dir string

// Namespace returns the namespace of the Pod Mirage runs in, which is always the
// Target Namespace. It is not configurable — see ADR 0001 and the Deferred
// section of TODO.md.
func (d Dir) Namespace() (string, error) {
	path := filepath.Join(string(d), "namespace")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Target Namespace from %s: %w", path, err)
	}
	ns := strings.TrimSpace(string(raw))
	if ns == "" {
		return "", fmt.Errorf("Target Namespace file %s is empty", path)
	}
	return ns, nil
}

// CertPool returns the cluster CA bundle, used to verify Upstream.
// Without this we have to either add the cluster certs into the system CA certs.
// Or, we would have to add InsecureSkipVerify to ignore verifying certs.
func (d Dir) CertPool() (*x509.CertPool, error) {
	path := filepath.Join(string(d), "ca.crt")
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA from %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("cluster CA %s contains no usable certificates", path)
	}
	return pool, nil
}
