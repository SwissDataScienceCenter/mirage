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
- A WATCH carrying `sendInitialEvents=true` — a **WatchList** — is answered with a `Bookmark`
  annotated `k8s.io/initial-events-end: "true"`, immediately. client-go 1.32 and later open one of
  these in place of a LIST followed by a WATCH: the server streams the collection's current contents
  as `ADDED` events and then marks the end of that burst, and the reflector treats the marker as
  permission to report itself synced. A masked collection is empty, so the Bookmark *is* the burst.

The last of those is the sharpest illustration of why this ADR exists. Omitting the annotation does
not produce an error anywhere: the reflector simply waits, logging `awaiting required bookmark event
for initial events stream` every ten seconds while the Client's informers never sync and its
controller never starts. Nothing in Mirage's own logs looks wrong — the request arrives, is
classified `mask`, and is answered `200`.

It was missed when this ADR was first written, because client-go did not yet do it, and it was found
by the integration suite ([ADR 0007](./0007-envtest-for-the-integration-tier.md)) rather than by
reading. That is the argument for the integration tier in one bug: imitation has to be judged against
the real client, at the version the real client is actually at, and the set of things client-go
expects grows over time.

Masking is supported for **custom resources only**. Mirage replies to a Masked Resource in JSON,
and returns `406 Not Acceptable` if the Client demands `application/vnd.kubernetes.protobuf` —
which is exactly what a real API server does for custom resources, so a well-behaved client falls
back to JSON. That equivalence does not hold for built-in types, where client-go negotiates
protobuf by default and would not fall back. Masking a built-in type therefore requires implementing
protobuf encoding first.
