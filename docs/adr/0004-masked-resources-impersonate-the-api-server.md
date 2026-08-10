# Masked Resources are answered locally, faithfully, and only for custom resources

For a Masked Resource, Mirage never contacts Upstream. It answers LIST with an empty
`<Kind>List` at a constant `resourceVersion` of `1`, answers WATCH with a chunked `200` that emits
no events, answers `GET .../name` with `404`, and answers mutations with `403`.

Where a choice existed, we chose to imitate the real API server rather than to do the simplest
thing:

- WATCH honours the `timeoutSeconds` query parameter and closes cleanly at expiry, and emits
  `Bookmark` events when `allowWatchBookmarks=true`. Holding the connection open forever is less
  code but puts a client-go reflector into a state the real API server never produces — the
  reflector sets a randomised 5–10 minute timeout and expects the server to close so it can re-LIST.
- Errors are well-formed `metav1.Status` objects, because client-go decodes these to build its
  typed errors; a bare string body yields an uninformative failure in the Client's logs.

Masking is supported for **custom resources only**. Mirage replies to a Masked Resource in JSON,
and returns `406 Not Acceptable` if the Client demands `application/vnd.kubernetes.protobuf` —
which is exactly what a real API server does for custom resources, so a well-behaved client falls
back to JSON. That equivalence does not hold for built-in types, where client-go negotiates
protobuf by default and would not fall back. Masking a built-in type therefore requires implementing
protobuf encoding first.
