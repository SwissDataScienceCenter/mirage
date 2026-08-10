//go:build integration

// Package integration_test runs Mirage against a real Kubernetes API server.
//
// envtest starts real etcd and kube-apiserver binaries as child processes on
// loopback. It is not a fake and not a container, which is the point: Mirage's
// whole job is URL manipulation against a real API server's routing, and a fake
// would only re-assert whatever we already believe about that routing. See
// ADR 0007.
//
// The suite is behind the `integration` build tag, so `just test` neither
// compiles nor runs it. Run it with `just test-integration`, which puts the
// control-plane binaries on KUBEBUILDER_ASSETS first.
//
// There is deliberately no `if KUBEBUILDER_ASSETS == "" { Skip() }` guard. ADR
// 0006 exists because the streaming behaviour fails silently; a suite that skips
// itself when its binaries are missing reproduces that exact failure mode in the
// harness. Missing binaries are a hard error.
package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
	"github.com/SwissDataScienceCenter/mirage/internal/server"
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

const (
	// targetNamespace is the namespace Mirage presents to the Client as the whole
	// cluster — in production, the namespace of the Pod it is a sidecar in.
	targetNamespace = "mirage-target"
	// foreignNamespace holds the objects a confined request must not see. Nothing
	// grants the test user access to it, so it also proves the confinement is
	// doing the work rather than RBAC quietly hiding the objects anyway.
	foreignNamespace = "mirage-foreign"

	// The Client's identity. envtest authenticates its own admin client with a
	// TLS client certificate, which Mirage cannot forward — it forwards an
	// Authorization header and holds no credentials of its own (ADR 0001). So the
	// API server is given a static token file and the test Client uses a bearer
	// token, which is exactly the production arrangement.
	testUser  = "mirage-test-user"
	testToken = "mirage-integration-token"
)

var (
	// adminCfg is envtest's own cert-based client, in system:masters. Used for
	// arranging the world — namespaces, RBAC, objects — never for the assertions,
	// which go through mirageCfg.
	adminCfg *rest.Config
	// mirageCfg reaches the API server through Mirage: plaintext loopback, bearer
	// token, no TLS (ADR 0002).
	mirageCfg *rest.Config
	// directCfg is the same identity talking straight to the API server, so a spec
	// can show what Mirage changed.
	directCfg *rest.Config

	adminClient  *kubernetes.Clientset
	adminDynamic *dynamic.DynamicClient

	testEnv *envtest.Environment
	mirage  *httptest.Server
)

var (
	widgetGVR        = schema.GroupVersionResource{Group: "mirage.test", Version: "v1", Resource: "widgets"}
	clusterWidgetGVR = schema.GroupVersionResource{Group: "mirage.test", Version: "v1", Resource: "clusterwidgets"}
)

// mirageConfig is what Mirage is told about the two test resources: the
// namespaced one is Confined, the cluster-scoped one is Masked.
func mirageConfig() config.Config {
	return config.Config{
		Confined: []config.Resource{
			{Group: widgetGVR.Group, Plural: widgetGVR.Resource},
		},
		Masked: []config.Masked{
			{
				Resource: config.Resource{Group: clusterWidgetGVR.Group, Plural: clusterWidgetGVR.Resource},
				Kind:     "ClusterWidget",
			},
		},
	}
}

var _ = BeforeSuite(func(ctx SpecContext) {
	startControlPlane()
	createNamespaces(ctx)
	grantTargetNamespaceOnly(ctx)
	startMirage()
})

// startControlPlane brings up etcd and kube-apiserver, installs the test CRDs and
// resolves the three client configurations.
func startControlPlane() {
	tokenFile := writeTokenFile()

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("testdata", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	apiServer := testEnv.ControlPlane.GetAPIServer()
	// The wiring that makes the whole suite possible: a static token file, so the
	// Client can authenticate with something Mirage is able to forward.
	apiServer.Configure().Append("token-auth-file", tokenFile)
	// Set rather than inherited. envtest's default is RBAC today, but the RBAC
	// spec is meaningless under AlwaysAllow and would pass silently, so the mode
	// the suite depends on is stated here instead of assumed.
	apiServer.Configure().Set("authorization-mode", "RBAC")

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred(), "envtest failed to start; are the control-plane binaries on KUBEBUILDER_ASSETS? Run `just test-integration`")
	Expect(cfg).NotTo(BeNil())

	// A missed Stop() leaks etcd and kube-apiserver processes.
	DeferCleanup(func() {
		Expect(testEnv.Stop()).To(Succeed())
	})

	adminCfg = cfg
	adminClient, err = kubernetes.NewForConfig(adminCfg)
	Expect(err).NotTo(HaveOccurred())
	adminDynamic, err = dynamic.NewForConfig(adminCfg)
	Expect(err).NotTo(HaveOccurred())


	// AnonymousClientConfig keeps the CA and drops the client certificate, which
	// is the difference between "envtest's admin" and "somebody holding a token".
	directCfg = rest.AnonymousClientConfig(adminCfg)
	directCfg.BearerToken = testToken
}

// writeTokenFile writes the CSV --token-auth-file expects: token, username, uid.
func writeTokenFile() string {
	dir, err := os.MkdirTemp("", "mirage-envtest-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(os.RemoveAll(dir)).To(Succeed())
	})

	path := filepath.Join(dir, "tokens.csv")
	line := fmt.Sprintf("%s,%s,%s-uid\n", testToken, testUser, testUser)
	Expect(os.WriteFile(path, []byte(line), 0o600)).To(Succeed())
	return path
}

func createNamespaces(ctx context.Context) {
	// envtest runs no kube-controller-manager, so a deleted namespace never leaves
	// Terminating. These are created once and never deleted; specs keep their
	// objects apart with unique names instead.
	for _, name := range []string{targetNamespace, foreignNamespace} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		_, err := adminClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			continue
		}
		Expect(err).NotTo(HaveOccurred())
	}
}

// grantTargetNamespaceOnly binds the test user to a Role in the Target Namespace
// and nothing else. This is the production shape of the problem Mirage solves:
// the Client has namespace-scoped permissions and issues cluster-wide requests.
//
// It is also what makes the RBAC spec meaningful — the same cluster-wide LIST
// that succeeds through Mirage is a 403 straight to the API server, because no
// ClusterRoleBinding exists to allow it.
func grantTargetNamespaceOnly(ctx context.Context) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "widget-reader", Namespace: targetNamespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{widgetGVR.Group},
			Resources: []string{widgetGVR.Resource},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}},
	}
	_, err := adminClient.RbacV1().Roles(targetNamespace).Create(ctx, role, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())

	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "widget-reader", Namespace: targetNamespace},
		Subjects: []rbacv1.Subject{{
			Kind:     rbacv1.UserKind,
			APIGroup: rbacv1.GroupName,
			Name:     testUser,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     role.Name,
		},
	}
	_, err = adminClient.RbacV1().RoleBindings(targetNamespace).Create(ctx, binding, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
}

// startMirage builds the real server with server.New and puts it on a random
// loopback port.
func startMirage() {
	upstreamURL, err := url.Parse(adminCfg.Host)
	Expect(err).NotTo(HaveOccurred())

	// Mirage's transport verifies the cluster CA and carries nothing else — no
	// client certificate, no token. If header forwarding regresses, every proxied
	// request arrives anonymous and the suite fails loudly, which is the whole
	// reason the token file exists rather than reusing envtest's cert transport.
	transport, err := rest.TransportFor(rest.AnonymousClientConfig(adminCfg))
	Expect(err).NotTo(HaveOccurred())

	e, err := server.New(server.Options{
		Config:          mirageConfig(),
		TargetNamespace: targetNamespace,
		Upstream:        upstreamURL,
		Transport:       transport,
		Logger:          slog.New(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	Expect(err).NotTo(HaveOccurred())

	// httptest gives a listener on a free loopback port with no timeouts of its
	// own, which matters: a WriteTimeout here would tear down every WATCH. Note
	// that this means the suite exercises server.New but not the http.Server
	// settings cmd/mirage/main.go applies for the same reason — see TODO.md.
	mirage = httptest.NewServer(e)
	DeferCleanup(mirage.Close)

	mirageCfg = &rest.Config{Host: mirage.URL, BearerToken: testToken}
}

// names are unique per spec because `just test` runs --randomize-all and nothing
// in envtest garbage-collects between specs.
var nameCounter atomic.Int64

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, nameCounter.Add(1))
}

// widget builds an unstructured Widget.
func widget(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": widgetGVR.Group + "/" + widgetGVR.Version,
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{"note": "created by the integration suite"},
	}}
}

// createWidget creates a Widget with the admin client and removes it when the
// spec ends. Arranging the world is the admin's job; the assertions go through
// Mirage.
func createWidget(ctx context.Context, namespace, name string) *unstructured.Unstructured {
	GinkgoHelper()

	created, err := adminDynamic.Resource(widgetGVR).Namespace(namespace).
		Create(ctx, widget(namespace, name), metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())

	DeferCleanup(func(ctx SpecContext) {
		err := adminDynamic.Resource(widgetGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
	})

	return created
}

// mirageClient builds a dynamic client that reaches the API server through Mirage.
func mirageClient() *dynamic.DynamicClient {
	GinkgoHelper()

	c, err := dynamic.NewForConfig(mirageCfg)
	Expect(err).NotTo(HaveOccurred())
	return c
}

// through issues a raw request to Mirage with the Client's bearer token. Several
// specs need a request client-go will not produce — the legacy /watch/ path, a
// protobuf-only Accept — so they build it by hand.
func through(ctx context.Context, method, path string, header http.Header) *http.Response {
	GinkgoHelper()

	req, err := http.NewRequestWithContext(ctx, method, mirage.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	// No timeout on the client: a WATCH is meant to stay open, and the specs that
	// issue one bound it themselves with timeoutSeconds or a context.
	resp, err := (&http.Client{}).Do(req)
	Expect(err).NotTo(HaveOccurred())
	// Closed, not drained. Draining a WATCH that is still open would block until
	// its timeoutSeconds elapsed.
	DeferCleanup(func() { _ = resp.Body.Close() })

	return resp
}

// eventuallyTimeout is generous: the first request of a spec may wait on the API
// server's own caches, and CI is slower than a laptop.
const eventuallyTimeout = 30 * time.Second
