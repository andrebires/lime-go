# ADR 0001: LIME 2.0 implementation profile

- Status: Accepted
- Date: 2026-10-07
- Task: LIME2-T01
- Authority: explicit user implementation request and approval of profile choices.

## Context and decision

Implement the selected rules in the fast-chat LIME 2.0 v0.1 draft in an explicit
root `lime` package under module `github.com/andrebires/lime-go/v2`. The user
explicitly waived legacy API compatibility. Remove obsolete factories, channels,
legacy transports and examples; the 2.0 API is deliberately breaking.
This is an experimental implementation profile, not an official protocol release.
No fast-chat runtime or accepted fast-chat ADR is changed.

## Detailed design and invariants

- WebSocket subprotocol `lime`, exactly one JSON envelope per text message. No
  binary frames. HTTP-authenticated new -> established; otherwise new ->
  authenticating -> established using the historical `plain` scheme and its
  base64 password convention. Authentication callbacks bind identity; missing
  credentials never become implicit guest access. TLS is required outside local
  demos. Every new connection gets a fresh server-issued random session ID.
- `version: 2` is mandatory on new. No negotiation/resume. Both endpoints support
  text concatenation and RFC 7396 JSON Merge Patch, starting JSON assembly at {}.
- Strict field presence classification; reject competing families, duplicate keys
  (including nested JSON), unknown fields, invalid UTF-8, wrong types, invalid
  lifecycle fields, revisions outside 1..9007199254740991 and event/scope pairs.
  Wire reason codes for this profile: 100 invalid input, 101 unsupported version,
  102 authentication, 103 limit, 104 conflict, 105 unsupported operation. These
  profile-local codes are not claims about an official registry.
- Built-in aliases match the draft table. Custom aliases use command set at
  /protocol/aliases with type json and resource {"name":"canonical/type+json"}.
  Get returns the custom registry. Registration is additive, atomic, bounded,
  idempotent for identical mappings, cannot replace built-ins, requires a supported
  registered content type, and takes effect after a success response. Registries
  are connection-local. Custom and vendor content types require an explicit application validator;
  recognition of a built-in alias alone never grants content-schema support.
  Canonical nested MIME strings are not rewritten.
- ID-less complete messages are fire-and-forget, outside receipt/retry buffers.
  Retries preserve (id, rev), replay a whole complete message, and never cache or
  append old chunks. A duplicate completed revision is suppressed by the receiver
  within a bounded session deduplication window. Same revision with different
  content or routing is a conflict. Digests normalize JSON property order,
  whitespace and string escaping while preserving numeric literals exactly. Active streaming IDs are scoped by sender.
- Session receipts apply to one direction and peer. Message receipts clear just
  that entry; consumed and failed do not clear receipt buffers. Cumulative receipt
  cannot cross unfinished/failed entries. An explicit application Resolve removes
  a failed entry without representing it as received; both peers must agree on
  resolution before crossing it. Consumed thread progress uses a separate bounded
  delivery tracker and checks read gaps. Duplicate older watermarks are harmless
  within retained marker history; unknown markers fail. Progress maps each delivery
  revision to send order, never lexical IDs or display order.
- Retry scheduling is caller-controlled through Pending; network writes and retry
  attempts have deadlines and finite caller-selected limits. Capacity exhaustion
  rejects new work and never evicts unacknowledged entries. New sessions do not
  inherit aliases, unfinished assembly or automatic retry state.
- Limits default to 64 KiB frames, 1 MiB assembled content, 32 active streams,
  256 dedup/receipt entries, 8 MiB pending retry payloads, 32 aliases, depth 64. No unbounded queues or per-token
  goroutines. Completed deduplication retains content digests, not full payloads;
  received/resolved retry payloads are released immediately. A synchronous writer owns its reusable encoding buffer. Ownership gates are
  cancelable; queued cancellation does not interrupt the current owner. Context
  cancellation closes the connection via context.AfterFunc, unblocking I/O without
  concurrent deadline mutation. Connection cancellation is terminal.
- End is issued only after the application's completion callback succeeds.
  Receiver callbacks run before completion is recorded/received is permitted.
  The demo is ephemeral and explicitly reports no durable storage; production
  applications must commit before confirming completion.

## Rejected alternatives and consequences

No binary encoding, reflection-driven factories, shared mutable registry, unsafe
string aliasing, goroutine per chunk, implicit downgrade, or in-memory durability
claims. Explicit raw JSON preserves exact numbers and avoids document factories;
applications own schemas/rendering, storage and per-operation authorization.
Existing consumers must migrate explicitly to the v2 module and new API. Performance claims require benchmarks and have workload limits.

## Failure, security, privacy and observability

Malformed frames close the connection; lifecycle/type/schema errors fail the
operation and discard the affected stream. No receipt for an incomplete stream.
Peer input cannot override bound from/pp identity. The local demo assigns random
identities via short-lived server-held capabilities, uses same-origin upgrade,
finite peers, and excludes any production account/tenant authority.
Expose errors, pending/active counts and benchmark/test evidence without logging
credentials. Demo shows protocol envelopes, intermediate content and failures.

## Validation

Independent wire fixtures, RFC 7396 examples, interruption/duplicate/revision/gap
contracts, WebSocket auth/version/cancellation/binary/limit tests, multi-client demo
tests, race/vet/build, >=90% diff coverage, fuzz seeds and allocation benchmarks.
