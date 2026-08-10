# Mirage

A proxy that sits between a Kubernetes controller and the Kubernetes API server, presenting a
single namespace as if it were the whole cluster. It exists so that operators whose charts and
code assume cluster-wide access can run with namespace-scoped permissions only.

## Language

**Mirage**:
The proxy itself. Runs as a sidecar container in the Client's Pod.
_Avoid_: Shim, interceptor, gateway

**Client**:
The controller or operator whose API calls Mirage intercepts. Reaches Mirage over `localhost`
via the `KUBERNETES_SERVICE_HOST` / `KUBERNETES_SERVICE_PORT` environment variables.
_Avoid_: Operator, controller, consumer

**Upstream**:
The real Kubernetes API server that Mirage forwards to.
_Avoid_: Real API, backend, origin

**Deployer**:
The person installing the Client, who applies the kustomize patch that adds Mirage to the Client's
Pod. Has authority within one namespace only — that limitation is the reason Mirage exists.
_Avoid_: Admin, operator, user

**Target Namespace**:
The single namespace Mirage presents to the Client as though it were the whole cluster. Always the
namespace of the Pod Mirage runs in.
_Avoid_: Scoped namespace, tenant namespace, watched namespace

**Confine**:
To insert the Target Namespace into a request path that names no namespace, so a request for a
resource across every namespace becomes a request for it in the Target Namespace alone. Mirage's
only transformation, and a URL-only one — the payload is never touched, per ADR 0003. A path that
already names a namespace is never confined, not even to correct a foreign one: leaving it alone
keeps the API server the sole judge of whether it is allowed.
_Avoid_: Handle, scope, redirect. "Rewrite" describes the mechanism — the path is rewritten — but
is too vague to name the decision.

**Confined Resource**:
A resource kind that Mirage's configuration names as one to Confine. A cluster-wide `LIST` or
`WATCH` of a Confined Resource is confined to the Target Namespace; requests that already name a
namespace, name a single object, or address a subresource are Passed Through untouched. Only
Confined Resources are confined; anything else is Passed Through.

Configuration names them under `confined:`. A cluster-scoped resource cannot be confined — there is
no namespaced path to confine it into — so Mirage refuses to start with `namespaces` listed there;
such a resource is a candidate for Masking instead.
_Avoid_: Handled resource, watched resource, managed resource, intercepted resource

**Masked Resource**:
A resource kind that Mirage answers itself rather than forwarding, always reporting it as existing
but empty. Typically a cluster-scoped custom resource the Client reads but does not need.
_Avoid_: Faked resource, stubbed resource, blocked resource

**Pass Through**:
To forward a request to Upstream unchanged. The default for any request Mirage is not configured to
Confine or Mask.
_Avoid_: Ignore, bypass, proxy verbatim
