# Mirage serves HTTPS on loopback with a self-signed certificate

Mirage listens on `127.0.0.1` over HTTPS, using a certificate it generates at startup and holds
only in memory. The Client is repointed at it with a mounted kubeconfig — `server:
https://127.0.0.1:<port>`, `insecure-skip-tls-verify: true`, `tokenFile` pointing at the projected
ServiceAccount token — selected via the `KUBECONFIG` environment variable.

Nothing verifies that certificate. It exists because of a single line in `clientcmd`:

```go
// only try to read the auth information if we are secure
if restclient.IsConfigTransportTLS(*clientConfig) {
    ...
    userAuthPartialConfig, err := config.getUserIdentificationPartialConfig(...)
```

`IsConfigTransportTLS` is `baseURL.Scheme == "https"`. Against an `http://` server the whole
credential block is skipped — `token`, `tokenFile`, client certificates, all of it — with no error
and no warning. The Client builds a `rest.Config` with no bearer token, every request arrives at
the API server as `system:anonymous`, and discovery fails on a `403` before the controller's manager
can start.

That defeats Mirage's central mechanism. [ADR 0001](./0001-mirage-never-adds-authority.md) has
Mirage hold no credentials of its own and forward the Client's; a transport the Client will not send
credentials over leaves nothing to forward. So TLS here is not a security measure — the listener is
on loopback inside a shared Pod network namespace and is reachable only by containers in that same
Pod, exactly as before — it is the price of admission for the Client's token.

## Why skip verification rather than distribute a CA

Verification would have to be worth something, and here it cannot be. The peer is another container
in the same network namespace; anything able to intercept the connection is already inside the Pod
and holds the token anyway. Distributing a CA to make that check meaningful would mean writing a
bundle where the Client can read it and keeping the two in step, which is cost paid for no property
gained.

The alternative considered and not taken: Mirage writes its CA — and the whole kubeconfig — into a
shared `emptyDir` at startup, and the Client's `KUBECONFIG` points there. It works, and it has the
real merit of deleting the hand-maintained kubeconfig from the ConfigMap, where today it can drift
out of step with the listener. It was left for later because it turns a static, reviewable
ConfigMap into a file generated at runtime, and because the verification it enables is worth
nothing. If the kubeconfig drifting proves to be a recurring mistake, that is the fix.

## What this does not solve

`rest.InClusterConfig()` hardcodes both the `https://` scheme and the CA bundle path
`/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`, so a Client using it verifies Mirage's
certificate against the *cluster* CA and cannot be told otherwise. Obtaining a certificate from that
CA requires CSR signer and approval permissions that Mirage's target users, by definition, do not
have. Supporting such a Client needs a projected volume overriding `ca.crt`, which is a different
piece of work.

Consequence, unchanged from before: this only works for Clients that honour `KUBECONFIG` — anything
using controller-runtime's `GetConfig()`. A Client calling `rest.InClusterConfig()` directly still
ignores it.

## History

This ADR originally recorded the opposite decision: plaintext HTTP on loopback, on the reasoning
that TLS across a Pod-local connection protects against nothing. That reasoning was sound and the
conclusion was still wrong, because it treated the scheme as a security question when `clientcmd`
treats it as a gate on whether credentials exist at all. The cost of the mistake was a Client that
failed at startup with `failed to get server groups: unknown` — client-go's discovery path
substitutes that word for any non-2xx response, so the API server's perfectly clear
`forbidden: User "system:anonymous" cannot get path "/api"` never reached the logs.
