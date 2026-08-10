// Package decide turns an inbound request into one of three decisions: Pass
// Through, Rewrite, or Mask.
//
// Deciding is a pure function of the method, the path and the configuration. It
// never touches the payload — see ADR 0003 — so the only transformation Rewrite
// performs is inserting the Target Namespace into the URL.
package decide

import (
	"net/http"
	"strings"

	"github.com/SwissDataScienceCenter/mirage/internal/config"
)

// Action is what Mirage does with a request.
type Action string

const (
	// PassThrough forwards the request to Upstream unchanged. The default.
	PassThrough Action = "pass-through"
	// Rewrite inserts the Target Namespace into the path and forwards it.
	Rewrite Action = "rewrite"
	// Mask answers the request locally without contacting Upstream.
	Mask Action = "mask"
)

// Target is a Kubernetes API path broken into its parts.
//
// Recognised shapes, for the core group and for named groups respectively:
//
//	/api/{version}/[namespaces/{namespace}/]{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/[namespaces/{namespace}/]{resource}[/{name}[/{subresource}]]
type Target struct {
	Group       string
	Version     string
	Namespace   string
	Resource    string
	Name        string
	Subresource string

	// Namespaced reports whether the path carried a namespaces/{namespace} segment.
	// A path without one addresses either the whole cluster or a cluster-scoped
	// resource; nothing in the path distinguishes the two.
	Namespaced bool
	// OK reports whether the path parsed as a resource path at all. Discovery,
	// /healthz, /openapi and friends do not.
	OK bool
}

// Ref is the Target's identity for configuration lookup — version deliberately
// excluded, since a config entry applies to every version of a resource.
func (t Target) Ref() config.Resource {
	return config.Resource{Group: t.Group, Resource: t.Resource}
}

// Collection reports whether the path addresses a collection rather than a single
// object or one of its subresources.
func (t Target) Collection() bool { return t.Name == "" }

// Parse breaks a request path into its parts. A path it does not recognise comes
// back with OK false, which always means Pass Through.
func Parse(path string) Target {
	segments := strings.Split(strings.Trim(path, "/"), "/")

	var t Target
	var rest []string
	switch {
	case len(segments) >= 2 && segments[0] == "api":
		t.Version, rest = segments[1], segments[2:]
	case len(segments) >= 3 && segments[0] == "apis":
		t.Group, t.Version, rest = segments[1], segments[2], segments[3:]
	default:
		return Target{}
	}

	// A "namespaces" segment is only a namespace selector when something follows
	// the namespace name. /api/v1/namespaces/foo addresses the Namespace object
	// itself, not a resource within it.
	//
	// This is deliberately coarser than the API server, which treats "status" and
	// "finalize" as subresources of the Namespace object rather than as resources
	// within it — so it reads /api/v1/namespaces/foo/finalize as Namespace foo,
	// subresource finalize, where we read it as resource "finalize" in namespace
	// foo. The two agree on what Mirage does with it: no config entry can name a
	// resource called "status" or "finalize", so it is Passed Through unchanged
	// either way, and the namespace segment keeps it out of the warning heuristic.
	// Worth knowing before adding a rule that keys off Resource alone.
	if len(rest) >= 3 && rest[0] == "namespaces" {
		t.Namespaced, t.Namespace, rest = true, rest[1], rest[2:]
	}

	if len(rest) == 0 || rest[0] == "" {
		return Target{}
	}

	t.OK = true
	t.Resource = rest[0]
	if len(rest) >= 2 {
		t.Name = rest[1]
	}
	if len(rest) >= 3 {
		t.Subresource = strings.Join(rest[2:], "/")
	}
	return t
}

// Decision is the outcome for one request.
type Decision struct {
	Action Action
	// Path is what to send Upstream. Equal to the inbound path unless the Action
	// is Rewrite; meaningless when it is Mask.
	Path   string
	Target Target
	// Masked carries the configuration entry when the Action is Mask.
	Masked config.Masked
	// Warn marks a cluster-wide collection request for a resource that is not
	// configured. Such a request is Passed Through and will very likely come back
	// 403 — which is the signature of a missing handled entry.
	Warn bool
}

// Decider answers requests against a fixed configuration and Target Namespace.
// It is immutable once built and safe for concurrent use.
type Decider struct {
	namespace string
	handled   map[config.Resource]struct{}
	masked    map[config.Resource]config.Masked
}

// New builds a Decider. namespace is the Target Namespace: the namespace of the
// Pod Mirage runs in, and the one it presents to the Client as the whole cluster.
func New(cfg config.Config, namespace string) *Decider {
	d := &Decider{
		namespace: namespace,
		handled:   make(map[config.Resource]struct{}, len(cfg.Handled)),
		masked:    make(map[config.Resource]config.Masked, len(cfg.Masked)),
	}
	for _, r := range cfg.Handled {
		d.handled[r] = struct{}{}
	}
	for _, m := range cfg.Masked {
		d.masked[m.Resource] = m
	}
	return d
}

// Decide classifies one request.
func (d *Decider) Decide(method, path string) Decision {
	t := Parse(path)
	dec := Decision{Action: PassThrough, Path: path, Target: t}
	if !t.OK {
		return dec
	}

	if m, ok := d.masked[t.Ref()]; ok {
		dec.Action, dec.Masked = Mask, m
		return dec
	}

	if _, ok := d.handled[t.Ref()]; ok {
		// Insert the Target Namespace only when the path names no namespace and
		// addresses a collection. An explicit namespace — even a foreign one — is
		// left alone, so the API server stays the one deciding whether it is
		// allowed. Single-object and subresource paths cannot be namespace-less
		// for a namespaced resource, so they are left alone too.
		if !t.Namespaced && t.Collection() {
			dec.Action = Rewrite
			dec.Path = namespacedPath(t, d.namespace)
		}
		return dec
	}

	// Unconfigured. Pass it through, but say so if it looks like the request a
	// missing handled entry would produce.
	dec.Warn = method == http.MethodGet && !t.Namespaced && t.Collection()
	return dec
}

// Namespace returns the Target Namespace.
func (d *Decider) Namespace() string { return d.namespace }

func namespacedPath(t Target, namespace string) string {
	var b strings.Builder
	if t.Group == "" {
		b.WriteString("/api/")
	} else {
		b.WriteString("/apis/")
		b.WriteString(t.Group)
		b.WriteByte('/')
	}
	b.WriteString(t.Version)
	b.WriteString("/namespaces/")
	b.WriteString(namespace)
	b.WriteByte('/')
	b.WriteString(t.Resource)
	return b.String()
}
