# Mirage never adds authority

Mirage runs as a sidecar in the Client's Pod and forwards the Client's own `Authorization` header
to the Upstream API server untouched. It never reads its own ServiceAccount token, never mints
credentials, and never performs authorization decisions of its own — it confines *what* is being
asked for, never *who* is asking. The API server therefore remains the sole enforcement point, and
a broken or compromised Mirage can only cause the Client to receive `403`s, never to gain access
it did not already have.

The rejected alternative was for Mirage to hold its own (possibly broader) ServiceAccount token.
That would make Mirage a confused deputy listening on `localhost` inside a Pod that runs
third-party controller code, and would move authorization decisions out of the API server and into
Mirage. If a future change appears to require it, that is a signal the design has gone wrong.
