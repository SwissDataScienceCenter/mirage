# Echo's proxy middleware, and why WATCH streaming works without configuration

Mirage serves with Echo v5 and proxies with Echo's `middleware.Proxy` (single-target
`RoundRobinBalancer`), rather than constructing `httputil.ReverseProxy` itself. `/healthz` is an
ordinary Echo route; everything else goes to a catch-all `/*` carrying two route-level middlewares —
a `decide` middleware that confines the path to the Target Namespace, short-circuits Masked
Resources, or passes through,
followed by the proxy middleware. Echo's `Rewrite`/`RegexRewrite` options are left empty: they are
static patterns and cannot express Mirage's rule.

**The non-obvious part, recorded so nobody "fixes" it.** A Kubernetes WATCH is a long-lived chunked
response, and a buffering proxy would make a Client's informers receive nothing — a controller that
starts cleanly and then silently never reconciles. `httputil.ReverseProxy` buffers by default, and
Echo v5's `ProxyConfig` exposes no `FlushInterval` field and no hook to reach the underlying
`*httputil.ReverseProxy`, so we cannot configure it. It works anyway: `ReverseProxy.flushInterval`
returns "flush immediately" whenever `res.ContentLength == -1`, which is true of every WATCH, and
this is contractual rather than incidental — it is documented on the `FlushInterval` field itself.

Because that behaviour is load-bearing and invisible in Mirage's own source, **the end-to-end test
that runs a real client-go informer through Mirage and asserts events arrive is not optional**. It
is the only thing standing between a future Go or Echo upgrade and a silent production failure with
no error message anywhere. Do not delete it as redundant.
