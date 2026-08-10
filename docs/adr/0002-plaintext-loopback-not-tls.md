# Mirage serves plaintext HTTP on loopback

Mirage listens on `127.0.0.1` over plain HTTP, and the Client is repointed at it with a mounted
kubeconfig (`server: http://127.0.0.1:<port>`, `tokenFile` pointing at the projected ServiceAccount
token) selected via the `KUBECONFIG` environment variable.

Serving TLS would be the obvious choice for something impersonating the API server, but it is not
available to us: `rest.InClusterConfig()` hardcodes both the `https://` scheme and the CA bundle
path `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`, so a Client using it would verify
Mirage's certificate against the *cluster* CA — and obtaining a certificate from that CA requires
CSR signer/approval permissions that Mirage's target users, by definition, do not have. The
alternative is a self-signed cert plus a projected volume that overrides `ca.crt`, which works but
adds certificate generation, rotation and CA distribution to a project whose whole appeal is being
small. Since the listener is on loopback inside a shared Pod network namespace, it is reachable
only by containers in that same Pod, so TLS would protect against nothing.

Consequence: this only works for Clients that honour `KUBECONFIG` (e.g. anything using
controller-runtime's `GetConfig()`). A Client calling `rest.InClusterConfig()` directly will ignore
it, and supporting that requires the self-signed-cert mode described above.
