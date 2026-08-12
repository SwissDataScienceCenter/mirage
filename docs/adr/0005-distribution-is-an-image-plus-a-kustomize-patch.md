# Mirage ships as a container image and a documented kustomize patch

Mirage's entire distribution is a published container image plus documentation showing how to patch
a Client's Deployment with kustomize. We do not publish a Helm chart, a bundled operator, or an
injecting webhook. The patch adds Mirage to `initContainers` with `restartPolicy: Always` (a native
sidecar), mounts a ConfigMap holding Mirage's config and a kubeconfig pointing at
`https://127.0.0.1:<port>`, and sets `KUBECONFIG` on the Client's container.

A mutating webhook that injected the sidecar automatically was rejected outright: it needs a
cluster-scoped `MutatingWebhookConfiguration`, and a user who could create one would not need
Mirage in the first place. Everything Mirage does must be achievable by someone whose only
authority is within a single namespace.

**OLM-installed Clients are explicitly not supported.** OLM continuously reconciles an operator's
Deployment back to what its CSV declares, so a sidecar patch is reverted. `SubscriptionConfig` can
inject env vars and volumes into an OLM-managed Deployment but cannot add containers, so there is
no sidecar-shaped workaround; the remaining options are running Mirage as a separate Deployment
(which forfeits the loopback assumption that lets us skip certificate verification, and puts the
Client's bearer token on the network — see [ADR 0002](./0002-self-signed-tls-on-loopback.md)) or
forking the CSV into a custom catalog. Both are worse than saying no.
