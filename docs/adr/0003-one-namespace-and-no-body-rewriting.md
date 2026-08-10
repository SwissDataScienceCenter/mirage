# Exactly one Target Namespace, and Mirage never decodes payloads

Mirage presents a single namespace as the whole cluster. Rewriting is therefore purely a URL
transformation: Mirage never decodes, merges or re-encodes a payload that passes through it.

Note the asymmetry — this forbids *decoding* Upstream's bytes, not *producing* bytes. Mirage
synthesises complete responses of its own for Masked Resources, which is unaffected by this
decision precisely because those responses are made up rather than derived from a payload it had
to understand.

Presenting a *set* of namespaces was the alternative, and it is a different program. One inbound
cluster-wide LIST would become N Upstream LISTs that Mirage must decode, merge and re-encode,
which drags in: synthesising a `resourceVersion` from N independent ones (no correct value exists),
rewriting `continue` tokens for pagination, decoding protobuf as well as JSON (client-go negotiates
`application/vnd.kubernetes.protobuf` for built-in types by default), and multiplexing N watch
streams into one ordered stream that a Client will replay on reconnect. That is a partial
reimplementation of the API server's list-watch semantics, and getting it subtly wrong makes
informers miss events or hot-loop on `410 Gone`.

Because Mirage stays out of the payload, a LIST followed by a WATCH from the returned
`resourceVersion` behaves exactly as it would for a natively namespaced controller — reconnects
and bookmarks included — with no special handling at all.

This is a boundary, not a "not yet". Multi-namespace support should be a different project.
