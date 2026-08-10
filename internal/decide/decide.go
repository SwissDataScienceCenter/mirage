// Package decide turns an inbound request into one of three decisions: Pass
// Through, Confine, or Mask.
//
// Deciding is a pure function of the method, the path and the configuration. It
// never touches the payload — see ADR 0003 — so the only transformation Confine
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
	// Confine inserts the Target Namespace into a path that names no namespace,
	// turning a cluster-wide request into a namespaced one, and forwards it.
	Confine Action = "confine"
	// Mask answers the request locally without contacting Upstream.
	Mask Action = "mask"
)

// Target is a Kubernetes API path broken into its parts.
//
// Recognised shapes, for the core group and for named groups respectively:
//
//	/api/{version}/[watch/][namespaces/{namespace}/]{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/[watch/][namespaces/{namespace}/]{resource}[/{name}[/{subresource}]]
type Target struct {
	Group     string
	Version   string
	Namespace string
	// Plural is the resource segment of the path, always the lowercase plural —
	// "pods", "builds". Named to match config.Resource.Plural, which it is looked
	// up against.
	Plural string
	// Name is the object name, empty for a collection.
	Name        string
	Subresource string

	// Namespaced reports whether the path carried a namespaces/{namespace} segment.
	// A path without one addresses either the whole cluster or a cluster-scoped
	// resource; nothing in the path distinguishes the two.
	Namespaced bool
	// Watch reports whether the path carried the legacy /watch/ prefix, which is
	// how a Client asked for a watch before ?watch=true replaced it. Confining has
	// to put it back, and a Mask has to answer with a stream rather than a list.
	Watch bool
	// OK reports whether the path parsed as a resource path at all. Discovery,
	// /healthz, /openapi and friends do not.
	OK bool
}

// Ref is the Target's identity for configuration lookup — version deliberately
// excluded, since a config entry applies to every version of a resource.
func (t Target) Ref() config.Resource {
	return config.Resource{Group: t.Group, Plural: t.Plural}
}

// Collection reports whether the path addresses a collection rather than a single
// object or one of its subresources.
func (t Target) Collection() bool { return t.Name == "" }

// Namespaces reports whether the path addresses the core Namespace resource.
//
// Namespace is cluster-scoped: /api/v1/namespaces/{name} is one Namespace object,
// and there is no /api/v1/namespaces/{namespace}/namespaces for a cluster-wide
// request to be confined into. Parse already relies on this to read a namespaces
// segment as a selector; Decide relies on it to refuse a confinement that cannot
// exist.
//
// The group check matters: a CRD named "namespaces" in some other group is an
// ordinary resource and may well be namespaced.
func (t Target) Namespaces() bool { return t.Group == "" && t.Plural == namespacesResource }

// namespacesResource is the core Namespace resource, which appears in a path both
// as a namespace selector and as a resource in its own right.
const namespacesResource = "namespaces"

// namespaceSubresources are the subresources the API server registers on the
// Namespace object. The set is closed, which is what makes the ambiguity in Parse
// resolvable at all: one segment past a namespace name, a path is either one of
// these or a resource within that namespace.
//
// A CRD whose plural were literally "status" or "finalize" would be misread as a
// Namespace subresource. The API server has the same conflict and resolves it the
// same way.
var namespaceSubresources = map[string]bool{"status": true, "finalize": true}

// Parse breaks a request path into its parts. A path it does not recognise comes
// back with OK false, which always means Pass Through.
func Parse(path string) Target {
	segments := strings.Split(strings.Trim(path, "/"), "/")

	// The prefix names the API group and version. Anything else — /healthz,
	// /openapi/v2, /apis, / — is not a resource path at all.
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

	// The legacy watch prefix, deprecated in favour of ?watch=true but still
	// served. It sits between the version and everything else, so stripping it
	// here leaves the six shapes below unchanged: /api/v1/watch/namespaces/foo/pods
	// is the namespaced-collection shape, watched.
	//
	// A CRD whose plural were literally "watch" would be misread, the same trade
	// namespaceSubresources makes.
	if len(rest) > 0 && rest[0] == "watch" {
		t.Watch, rest = true, rest[1:]
	}

	// What remains is one of six shapes. The three with a namespace selector and
	// the three without share the same {resource}[/{name}[/{subresource}]] tail,
	// which is what parts handles for both arms below.
	//
	//	{resource}                                  /api/v1/pods
	//	{resource}/{name}                           /api/v1/nodes/node-1
	//	{resource}/{name}/{subresource...}          /api/v1/nodes/node-1/proxy/metrics
	//	namespaces/{ns}/{resource}                  /api/v1/namespaces/foo/pods
	//	namespaces/{ns}/{resource}/{name}           /api/v1/namespaces/foo/pods/nginx
	//	namespaces/{ns}/{resource}/{name}/{sub...}  /api/v1/namespaces/foo/pods/nginx/log
	//
	// /api/v1/namespaces and /api/v1/namespaces/foo need no arm of their own: they
	// are the first two shapes, with "namespaces" as an ordinary resource.
	switch {
	case len(rest) == 0:
		return Target{}

	// The one place two shapes collide. namespaces/{name}/{status|finalize} and
	// namespaces/{ns}/{resource} are structurally identical, so this resolves it
	// the way the API server does — by knowing which subresources the Namespace
	// object registers.
	case rest[0] == namespacesResource && len(rest) == 3 && namespaceSubresources[rest[2]]:
		t.Plural, t.Name, t.Subresource = rest[0], rest[1], rest[2]

	// A "namespaces" segment selects a namespace only when something follows the
	// namespace name. With nothing after it the path addresses the Namespace
	// object itself, and falls to the default arm.
	case rest[0] == namespacesResource && len(rest) >= 3:
		t.Namespaced, t.Namespace = true, rest[1]
		t.Plural, t.Name, t.Subresource = parts(rest[2:])

	default:
		t.Plural, t.Name, t.Subresource = parts(rest)
	}

	// Reachable from a path carrying empty segments, such as /api/v1//pods.
	if t.Plural == "" {
		return Target{}
	}

	t.OK = true
	return t
}

// parts splits the {resource}[/{name}[/{subresource}]] tail the six shapes share.
// A subresource can span several segments — pods/{name}/proxy/{path} is one — so
// it keeps the remainder whole.
func parts(rest []string) (plural, name, subresource string) {
	plural = rest[0]
	if len(rest) >= 2 {
		name = rest[1]
	}
	if len(rest) >= 3 {
		subresource = strings.Join(rest[2:], "/")
	}
	return plural, name, subresource
}

// Decision is the outcome for one request.
type Decision struct {
	Action Action
	// Path is what to send Upstream. Equal to the inbound path unless the Action
	// is Confine; meaningless when it is Mask.
	Path   string
	Target Target
	// Masked carries the configuration entry when the Action is Mask.
	Masked config.Masked
	// Warn marks a cluster-wide collection request for a resource that is not
	// configured. Such a request is Passed Through and will very likely come back
	// 403 — which is the signature of a missing confined entry.
	Warn bool
}

// Decider answers requests against a fixed configuration and Target Namespace.
// It is immutable once built and safe for concurrent use.
type Decider struct {
	namespace string
	confined  map[config.Resource]struct{}
	masked    map[config.Resource]config.Masked
}

// New builds a Decider. namespace is the Target Namespace: the namespace of the
// Pod Mirage runs in, and the one it presents to the Client as the whole cluster.
func New(cfg config.Config, namespace string) *Decider {
	d := &Decider{
		namespace: namespace,
		confined:  make(map[config.Resource]struct{}, len(cfg.Confined)),
		masked:    make(map[config.Resource]config.Masked, len(cfg.Masked)),
	}
	for _, r := range cfg.Confined {
		d.confined[r] = struct{}{}
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

	if _, ok := d.confined[t.Ref()]; ok {
		// Insert the Target Namespace only when the path names no namespace and
		// addresses a collection. An explicit namespace — even a foreign one — is
		// left alone, so the API server stays the one deciding whether it is
		// allowed. Single-object and subresource paths cannot be namespace-less
		// for a namespaced resource, so they are left alone too.
		//
		// Namespaces is excluded because no such path exists to confine it into.
		// config.Validate rejects it before startup, so this only guards a Decider
		// built from an unvalidated Config.
		if !t.Namespaced && t.Collection() && !t.Namespaces() {
			dec.Action = Confine
			dec.Path = confinedPath(t, d.namespace)
		}
		return dec
	}

	// Unconfigured. Pass it through, but say so if it looks like the request a
	// missing confined entry would produce.
	//
	// Never for Namespaces: it is cluster-scoped, so `confined` is not the fix and
	// suggesting it would send the Deployer towards a configuration Mirage refuses
	// to start with. A Client that lists namespaces wants `masked` or nothing.
	dec.Warn = method == http.MethodGet && !t.Namespaced && t.Collection() && !t.Namespaces()
	return dec
}

// Namespace returns the Target Namespace.
func (d *Decider) Namespace() string { return d.namespace }

// confinedPath rebuilds a namespace-less path with the Target Namespace inserted.
func confinedPath(t Target, namespace string) string {
	var b strings.Builder
	if t.Group == "" {
		b.WriteString("/api/")
	} else {
		b.WriteString("/apis/")
		b.WriteString(t.Group)
		b.WriteByte('/')
	}
	b.WriteString(t.Version)
	// Dropping this would turn the Client's watch into a one-shot list.
	if t.Watch {
		b.WriteString("/watch")
	}
	b.WriteString("/namespaces/")
	b.WriteString(namespace)
	b.WriteByte('/')
	b.WriteString(t.Plural)
	return b.String()
}
