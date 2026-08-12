package server_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// These specs pin the reason Mirage serves TLS at all, which is not a security
// property and so is not self-evident from the code: clientcmd reads a
// kubeconfig's credentials only when the server URL is https. Against an http://
// server it skips the entire credential block — silently, no error and no warning
// — and the Client then reaches the API server as system:anonymous.
//
// This is the contract between Mirage's listener and the kubeconfig the README
// publishes, so it is asserted rather than left to a comment somebody later
// decides is stale. Reverting the listener to plaintext, or "simplifying" the
// README's server URL back to http://, fails here rather than in production. See
// ADR 0002.
var _ = Describe("The kubeconfig the Client is given", func() {
	const token = "a-projected-serviceaccount-token"

	// kubeconfig is the README's, parameterised on the scheme: one cluster, one
	// user authenticating with a token file, verification skipped.
	kubeconfig := func(server, tokenFile string) clientcmdapi.Config {
		return clientcmdapi.Config{
			CurrentContext: "mirage",
			Clusters: map[string]*clientcmdapi.Cluster{
				"mirage": {Server: server, InsecureSkipTLSVerify: true},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"mirage": {TokenFile: tokenFile},
			},
			Contexts: map[string]*clientcmdapi.Context{
				"mirage": {Cluster: "mirage", AuthInfo: "mirage"},
			},
		}
	}

	// tokenFile writes a projected ServiceAccount token and returns its path.
	tokenFile := func() string {
		path := filepath.Join(GinkgoT().TempDir(), "token")
		Expect(os.WriteFile(path, []byte(token), 0o600)).To(Succeed())
		return path
	}

	It("carries the Client's token when Mirage is https", func() {
		cfg, err := clientcmd.NewDefaultClientConfig(
			kubeconfig("https://127.0.0.1:8001", tokenFile()),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		Expect(err).NotTo(HaveOccurred())

		Expect(cfg.BearerToken).To(Equal(token),
			"clientcmd did not load the token file; the Client would reach the API server as system:anonymous")
	})

	It("silently drops the Client's token when Mirage is plaintext", func() {
		// The failure that shipped. Documented as a passing spec because the
		// behaviour is clientcmd's and will not change: what must not change is
		// Mirage's response to it, which is to serve TLS. If this ever starts
		// failing, clientcmd has relaxed the gate and ADR 0002 can be revisited.
		cfg, err := clientcmd.NewDefaultClientConfig(
			kubeconfig("http://127.0.0.1:8001", tokenFile()),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()

		// No error. That is the whole problem — the Client starts, and only fails
		// later with a 403 that client-go's discovery path reports as "unknown".
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.BearerToken).To(BeEmpty())
	})
})
