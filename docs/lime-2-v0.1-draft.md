<!-- Source: fast-chat working-tree draft updated 2026-10-08; SHA-256 4ed690d2c0192ae374355996291926aa8b9c553d74f82590f23057e45cbb3f6e. Relative links point to source repository main, not immutable snapshots of those documents. -->

# LIME 2.0 — Specification v0.1 review draft

Date: **2026-10-07**. Status: **Draft for review; wire contract not frozen**.
Updated: **2026-10-08**, symmetric command streaming and RFC 6902 JSON Patch.

This document consolidates the selected direction from the
[exploration decision log](https://github.com/andrebires/fast-chat/blob/main/docs/specifications/lime-2-exploration.md). It is a proposal for an evolved
LIME protocol, not an official LIME release, a deployed implementation, or a claim
of compatibility with historical LIME clients.

Active research task: **LIME2-SPEC-01** in the [backlog](https://github.com/andrebires/fast-chat/blob/main/BACKLOG.md).
The historical M2-F02-T03 association remains absent; the log records it. Relevant
implementation work remains M2-F02-T01/T02. This draft does not change backlog
status, runtime code, or accepted ADRs. The review items at the end must be
resolved before claiming independent implementations will interoperate.

## 1. Purpose and scope

Provide fast, readable, JSON-based conversation communication suitable for
LLM-backed products. Minimize avoidable bytes, round trips, and recovery work
without making behavior implicit or ambiguous. No binary encoding is selected.

The protocol separates messages, notifications, commands, and session lifecycle.
A message is the fundamental conversation unit. Text and rich content may be
separate messages; there is no native multipart message abstraction.

Protocol expressiveness is separate from product rendering and implementation
scope. JSON streaming is defined even if a first implementation omits it. Rich
component rendering, model prompting, and business-action execution are product
concerns that use the protocol. No performance gain is claimed without measurement.

## 2. Transport and sessions

The initial transport is WebSocket with JSON envelopes. Sending and receiving
are independent. Response scheduling, interruption, and queueing are server
policy. Contributions rely on ordering within a connection; there are no public
chunk sequence numbers. Reconnect does not inherit that ordering guarantee.

When HTTP transport authentication has succeeded, the normal session path is
**`new` → `established`**. Otherwise the LIME server enforces
**`new` → `authenticating` → `established`**, completing the selected authentication
scheme before establishment. There is no mandatory
presence registration, receipt-enablement command, or alias exchange before
ordinary communication. Receipt behavior follows protocol conventions by default.

**Selected direction, 2026-10-07:** the general protocol may use LIME's optional
`negotiating` state to exchange content-streaming capabilities alongside its
existing encryption/compression negotiation. This project's profile skips that
capability exchange and uses documented support conventions, preserving the
short establishment path above. This records "not supported by our implementation"
as referring to capability negotiation, not a new decision to disable streaming.
Authentication remains required when the HTTP transport has not authenticated.
Section 7.1 separates this direction from the still-provisional capability grammar.

The server establishes the session only after binding it to an authenticated
identity and authorized scope, including anonymous capabilities where the product
allows them. For WebSocket connections, authentication may be performed by the
HTTP transport layer; the LIME server must use that verified identity and scope.
If transport authentication has not supplied a valid identity, LIME's
`authenticating` state is required. Missing authentication is not implicit guest
access, and failed authentication cannot transition to established.

HTTP authentication mechanisms are outside LIME. When fallback authentication
is needed, retain existing LIME authentication conventions without inventing a
new scheme. `new` is not a request for unrestricted access. Authenticated identity
does not remove per-operation authorization.

The initiator includes **`"version": 2`** in the new session envelope. This adds
version selection without another setup round trip. **Selected 2026-10-07:** an
omitted or unsupported version fails establishment: the server sends a session
envelope with `state: "failed"` and a LIME `reason`, then closes the connection.
There is no implicit downgrade. The exact reason code remains to be assigned.

Illustrative HTTP-authenticated happy path, using LIME's server-issued session
ID convention:

Client:

```json
{"state":"new","version":2}
```

Server, after the authorization boundary succeeds:

```json
{"id":"s1","state":"established"}
```

Content-type alias registration uses separate commands after establishment.
Protocol-resource commands and optional extensions do not add mandatory session
stages. The conditional `authenticating` exchange is independent of alias setup.
Retain existing LIME closing and rejection conventions; either node may terminate
the session/connection. There is **no session resume**: reconnection establishes
a new session with a new server-issued session ID. Thread/history continuity is
an application concern and does not require preserving a transport session.

## 3. Message identity and context

| Field | Meaning |
| --- | --- |
| `id` | Logical message identity; optional for complete fire-and-forget messages, required for streaming and correlated notifications/replacement. |
| `rev` | Message revision. Omission always means `1`. |
| `thread` | Shared conversational context grouping exchanged messages. |
| `type` | Content type, represented by canonical MIME name or defined alias. |
| `content` | Content value, or contribution when `stream` is `data`. |
| `stream` | Streaming lifecycle signal: `start`, `data`, or `end`. |

A thread can span many exchanges and model invocations. It is not a sender
identity and does not grant authorization. Thread binding and presentation of
authorship are application responsibilities; retain LIME's existing routing
conventions unless an application profile specifies thread addressing.

IDs are application-defined, following LIME's caller-selected identifier
convention. UUIDs are recommended to reduce collision risk, not required by the
protocol. The application must correlate messages and notifications correctly;
this draft does not impose a global ID namespace or ID-generation service.

Both peers track receipt progress by `(id, rev)`, interpreted within the
notification scope defined in section 5. The tuple identifies a message revision;
its values are not compared lexicographically to determine order.
Positions belong to messages, not transmission chunks. An updated revision is an
edit of the same message and retains its position in the displayed thread. This
does not make its delivery already acknowledged: the new revision has its own
`(id, rev)` identity for receipt tracking. Durable-history indexing and recovery
of edits are application implementation concerns, not a prescribed core log.

Revision allocation, concurrency policy, and edited-message presentation belong
to the application. Any envelope referring to a non-default revision must identify
it explicitly; omission does not inherit an active revision or mean latest.
The wire datatype/range is a small grammar matter, distinct from who allocates
revisions or how edits are displayed.

**Selected 2026-10-07:** `rev` is a JSON integer from `1`
through `9007199254740991`, with omission meaning `1`. Strings, null, fractional,
zero, negative, and out-of-range values are invalid. This keeps revisions within
the interoperable exact-integer range described by
[RFC 8259 section 6](https://www.rfc-editor.org/rfc/rfc8259.html#section-6).
No wraparound or implicit conversion is allowed; allocation remains application
policy.

## 4. Complete messages and streaming

**Selected 2026-10-07:** when `stream` is absent, the envelope contains a complete
message. Ordinary LIME-shaped messages remain valid:

```json
{"id":"m1","thread":"t1","type":"text","content":"Hello!"}
```

This single envelope needs no separate start or end. `id` is optional for this
form: a message with only `type` and `content` is also valid, with destination and
context resolved by the session/application conventions. Provisional wire example
using the canonical MIME spelling supported by LIME 1:

```json
{"type":"text/plain","content":"Hello!"}
```

An ID-less message is fire-and-forget: it cannot be the target of a correlated
notification, a `(id, rev)` watermark, or an edit of an identified message.
Streaming still requires `id` on start/data/end. This preserves the ordinary
LIME 1 message form; it does not promise full LIME 1 session or extension
compatibility. In particular, aliases and the version-2 handshake remain new.

Recommended receipt-buffer rule, awaiting confirmation: exclude ID-less messages
from the acknowledged-delivery sequence and retry buffer. A cumulative receipt
would cover earlier identified deliveries only, not retroactively turn
fire-and-forget traffic into acknowledged traffic. This boundary must be explicit
before implementations mix both forms on the same session.

Selected streaming signals:

| Signal | Required streaming fields | Assembly effect |
| --- | --- | --- |
| `start` | `id`, `stream`, `type` | Establish identity and content interpretation. No content. |
| `data` | `id`, `stream`, `content` | Apply one contribution using the established type. |
| `end` | `id`, `stream` | Confirm successful, persisted completion. No content. |

`rev` defaults to `1` on every envelope. Non-default revisions must be explicit
on start, data, and end. `type` is not repeated on data/end. It never signals a
lifecycle event. Stream end is not a Notification and is not merely a disconnected
transport, the last provider token, or a stopped generation.

Illustrative revision-2 stream:

```json
{"id":"m1","rev":2,"thread":"t1","type":"text","stream":"start"}
```

```json
{"id":"m1","rev":2,"stream":"data","content":"Hello again!"}
```

```json
{"id":"m1","rev":2,"stream":"end"}
```

The examples put thread metadata on start. The application associates subsequent
data/end with that message through its identifiers and routing context. The core
does not prescribe a thread lookup table or global ID scope. Applications must
avoid ambiguous correlation. A new connection does not continue an old session's
stream; any replay must not blindly append duplicate content.

### 4.1 Text

For `text/plain` (alias `text`), each data content is a JSON string. Concatenate
its decoded value in delivery order. Contributions can have variable sizes and
need not match provider tokens or provider event boundaries.

The server may batch and apply policy checks before release. A check that requires
the full response can delay publication. No buffering interval is standardized.

### 4.2 JSON

**Selected 2026-10-08:** streamed JSON uses **RFC 6902 JSON Patch**, replacing
RFC 7396 Merge Patch. Each data content is a complete operation array, not a
fragment of serialized JSON. Support `add`, `remove`, `replace`, `move`, `copy`,
and `test`, with RFC 6901 JSON Pointers. Arrays support insertion and `/-`
append; literal null is a value, while `remove` explicitly deletes a member.
Operations and contributions apply in delivery order. Unknown operation members
are ignored as specified by RFC 6902; unknown operations and invalid pointers fail.
See [RFC 6902](https://www.rfc-editor.org/rfc/rfc6902.html).

The final content type remains the component/document type, such as `select`;
it is not replaced with a patch MIME type. Aliases do not change assembly rules.

On a JSON start, initialize that revision's assembly to fresh `{}`. Start has no
content. Patches affect only the provisional content, never a previous completed
revision or envelope. `path: ""` addresses the root and permits array/scalar/null
replacement. Removing the root requires a later root `add` before successful end.
Empty streams produce `{}`, subject to the final content schema.

Apply each operation batch atomically from the consumer's perspective. An invalid
operation, missing target/parent, failed test, forbidden descendant move, or limit
violation discards the affected provisional stream; it must not produce successful
progress/completion/receipt. Profiles bound operations, accumulated work, result
size and depth, including `copy` expansion. Preserve prior completed revisions.
Patches are not generally idempotent: do not replay or retry individual data
contributions. Recovery sends complete content or restarts a fresh assembly.

Example data contributions after a start declaring `type: "select"`:

```json
{"id":"m2","stream":"data","content":[{"op":"add","path":"/text","value":"Choose a topic"},{"op":"add","path":"/options","value":[]}]}
```

```json
{"id":"m2","stream":"data","content":[{"op":"add","path":"/options/-","value":{"text":"Delivery"}}]}
```

```json
{"id":"m2","stream":"data","content":[{"op":"add","path":"/options/-","value":{"text":"Returns"}},{"op":"add","path":"/selection","value":null}]}
```

The array grows without retransmitting earlier options. A later end confirms the
assembled complete document. JSON Patch does not append substrings to string
values; use text streaming for token-like string contributions.

**Draft migration:** this changes structured-stream wire semantics. Update peers
together or select an explicit matching profile; do not infer/downgrade formats
from payload shape. Existing complete JSON and text messages retain their meaning.
This does not by itself provide AG-UI envelope or state-baseline compatibility.

JSON streaming implementation may be deferred. A receiver without support must
not treat partial updates as successful complete content. The recommended failure
behavior is one `failed` notification as soon as unsupported streaming is known,
followed by discarding the rejected stream's remaining contributions. Exact timing,
reason codes, and lifecycle cleanup remain review items.

Progressive rendering is optional product behavior. A renderer may wait until
the complete object has passed schema and business validation. Receiving a
component does not authorize an action.

## 5. Notifications and cumulative progress

**Selected, 2026-10-07:** Notifications separate `event` (what happened) from
`scope` (which messages it covers). Omitted `scope` means `message`. Cumulative
progress requires an explicit scope, as defined in section 5.2.

Only these message notification events are selected:

| Event | Meaning |
| --- | --- |
| `received` | Receipt of complete content, including terminal confirmation for a stream. |
| `consumed` | Read/seen in the UI. |
| `failed` | Failure with a required `reason`. |

Every notification targets `(id, rev)`; omitted `rev` means `1` for all three
events. A delayed receipt for revision 1 cannot acknowledge revision 2. There are
no `accepted`, `validated`, or `dispatched` notification stages, and no setup
command is needed to enable the selected receipt behavior.

A cumulative receipt requires the entire prefix through its marker to be
complete or explicitly resolved. A streamed message requires both complete content
and terminal confirmation. If A is incomplete and B finishes, receipt through B
cannot silently acknowledge A. Duplicate or older progress must not move the
cursor backward. Internal numeric positions may support this mapping.

Retain LIME's notification envelope and sender/destination conventions, with the
selected event subset and `rev` and `scope` extensions. `id`
identifies the referenced message; the existing routing context identifies the
participating nodes. Thread-scoped notifications must include `thread`. Failure `reason`
retains LIME's required integer `code` and optional string `description`.

Provisional wire examples, retaining LIME destination addressing. A session
receipt explicitly acknowledges a delivery prefix:

```json
{"id":"m1","rev":2,"to":"peer@example.org","event":"received","scope":"session"}
```

A thread read watermark includes the thread identifier:

```json
{"id":"m8","to":"peer@example.org","event":"consumed","scope":"thread","thread":"t1"}
```

Omitting scope acknowledges only the named message revision, even when thread
context is present. Both omitted `scope` and omitted `rev` use their defaults:

```json
{"id":"m8","to":"peer@example.org","event":"consumed","thread":"t1"}
```

Failure scope is the affected revision only, without advancing a
receipt cursor or failing earlier messages. A failed/incomplete earlier message
and a later cumulative receipt still need a shared rule; this is a delivery
semantic, not a requirement for a particular tombstone store. Product read
visibility and edited-message presentation remain application policy.

### 5.1 Sender retry buffer

Each sending node, whether client or server, keeps a bounded buffer of sent
messages not yet covered by a `received` notification for the corresponding
session. Section 4 proposes excluding ID-less fire-and-forget messages from this
buffer and its acknowledged sequence. A message-scoped receipt clears only its referenced entry. A
session-scoped receipt clears the acknowledged prefix through its exact
`(id, rev)` marker. Senders retry unacknowledged messages. Buffer capacity,
storage, retry timing, and scheduling are implementation decisions.

The buffer holds messages/revisions, not cached stream chunks. Recommended retry
identity is the same `(id, rev)` so a lost receipt need not create another logical
message. Whether pending messages survive a session change, and what happens on
capacity exhaustion, are not selected. In particular, eviction must not be
interpreted as successful receipt. At-least-once retries need an application
deduplication rule; this draft does not promise exactly-once delivery.

### 5.2 Notification scope

The selected `scope` values are `message`, `thread`, and `session`. Omitted
`scope` means `message`, preserving individual notification semantics by default.
This supersedes the earlier exploration examples that implied cumulative
behavior from `received` alone.

| Event | Allowed scopes | Meaning |
| --- | --- | --- |
| `received` | `session`, `message` | A contiguous session delivery prefix, or just the named message revision. |
| `consumed` | `thread`, `message` | A contiguous read prefix in the named thread, or just the named message revision. |
| `failed` | `message` | Failure of only the named message revision; reason required. |

A session receipt covers messages sent in one direction to the acknowledging
peer in the current session, across threads. It does not acknowledge command
responses, notifications, messages addressed to other recipients, or another
session. The sender resolves `(id, rev)` against its ordered message-delivery
buffer, not displayed thread position. Recommended retry behavior is to preserve
the original pending entry rather than create a second ambiguous watermark
position; retry identity remains a separate review item.

Thread scope requires `thread`; message scope may include thread as correlation
context without becoming cumulative. This avoids making presence of a routing
field implicitly change the acknowledgement's meaning. Reject invalid event/scope
combinations, unknown scope values, and unresolved or mismatched markers.

Session watermarks cannot pass incomplete earlier messages in that delivery
order. Cross-thread streaming therefore creates a trade-off: a long unfinished
message in one thread can delay cumulative receipt of later messages in another.
An individual `received` can acknowledge a later completed message without
advancing the contiguous watermark. A failure does not itself acknowledge the
prefix. The rule for resolving a failed entry remains to be selected.

Likewise, thread-wide `consumed` must not cover unread earlier messages. Use an
individual event when the UI has read gaps. Read progress and transport receipt
are separate; consumed/failed must not implicitly clear a cumulative send buffer.
Omitted `rev` remains 1, and no event acknowledges future revisions. Edited-message
read handling and the thread read-order mapping remain application contracts.

## 6. Content types and aliases

Aliases resolve to canonical MIME identities before content interpretation.
**Selected 2026-10-07:** start with every type defined on the
[LIME content-types page](https://limeprotocol.org/content-types.html), plus the
previously selected generic `json` mapping. This explicit table is the v0.1
snapshot; later website additions do not automatically change the protocol.

| Alias | Canonical type |
| --- | --- |
| `text` | `text/plain` |
| `json` | `application/json` |
| `chatstate` | `application/vnd.lime.chatstate+json` |
| `collection` | `application/vnd.lime.collection+json` |
| `document-select` | `application/vnd.lime.document-select+json` |
| `location` | `application/vnd.lime.location+json` |
| `media-link` | `application/vnd.lime.media-link+json` |
| `select` | `application/vnd.lime.select+json` |
| `web-link` | `application/vnd.lime.web-link+json` |

For the listed vendor types, strip `application/vnd.lime.` and `+json`, preserving
the remaining lowercase name and hyphens. Wire spelling follows those names
(`collection`, not `Collection`); this is the working interpretation of the
dictated naming rule. `text` and `json` retain their selected common mappings.
This is an enumerated registry, not permission to invent aliases by stripping
arbitrary MIME types. Alias recognition does not imply renderer or feature support.

Use the page's MIME declarations as the mapping source. Its Document select
example mistakenly uses the Select MIME name; the declaration is
`application/vnd.lime.document-select+json`. Adopting these names does not import
the page's schemas/examples verbatim or add unrelated command-resource types.

The JSON parser preserves the actual content value. A text string containing
JSON-looking characters stays text. A typed object supplied as serialized JSON
inside a string does not become that object through automatic second parsing.
Schema validation follows type resolution.

The alias map can be extended **by commands for a session**. Registration is not
part of session establishment. Extensions do not install renderers or authorize
unsupported content. Recommended rules are additive registration, immutable
built-in names, idempotent identical registration, and acknowledgement before use.
The command resource/payload, limits, and conflicts remain open. Extensions are
session-scoped; a new session starts with built-in aliases and any custom aliases
needed there must be registered again. There is no resumed session registry.

Nested MIME fields such as collection `itemType` are content-schema concerns.
Keep their canonical MIME spellings in examples until alias use in nested fields
is explicitly defined; do not rewrite arbitrary strings inside `content`.

## 7. Commands and extensions

Use LIME's command model as the baseline: request/response correlation by `id`,
resource addressing by `uri`, an operation in `method`, and typed `resource`
content when applicable. Responses use `status` and failure `reason`. This is the
recommended retained shape, not adoption of every historical method or resource.
See the [LIME command specification](https://limeprotocol.org/#command).

**Selected 2026-10-08:** the protocol supports streaming in both command requests
and command responses, for text strings and JSON values. An implementation may
support either direction, both, or neither according to its profile/capabilities.
The earlier recommendation to specify response streaming first is superseded.
The lifecycle and examples below define the proposed wire details for this
selected scope; they do not authorize runtime work.

The three-event notification vocabulary does not replace command response
statuses. There is no mandatory presence service or receipt-configuration
resource. Alias registration and other protocol-resource commands are extensions
with their own contracts; they do not enlarge the core establishment sequence.

Thread creation, history, subscription, cancellation, and alias-resource URIs and
schemas require a separate resource/profile definition. Avoid duplicating every
operation over both HTTP and LIME. A command being received does not establish
that a consequential business action succeeded.

### 7.0 Command streaming contract and examples

Reuse `stream: "start" | "data" | "end"`. Commands carry payload in `resource`,
never `content`. Keep `id` and `method` on every command contribution so it remains
identifiable without an envelope-kind field or a cross-family ID lookup. `type`
appears on start, `resource` only on data. Start/end carry no resource. Complete
commands without `stream` remain supported; either side may use the complete
form independently of whether the other side streams.

| Phase | Request | Response |
| --- | --- | --- |
| Start | `id`, `method`, `uri`, `type`, `stream: "start"` | `id`, `method`, `type`, `stream: "start"` |
| Data | `id`, `method`, `resource`, `stream: "data"` | `id`, `method`, `resource`, `stream: "data"` |
| End | `id`, `method`, `stream: "end"` | `id`, `method`, `stream: "end"`, `status`; `reason` required for failure |

Request end means that the input is complete, not that the operation succeeded.
Response end declares `status: "success"` or `"failure"`. Start/data response
envelopes omit status; this is an explicit streaming exception to LIME's ordinary
response requirement, not an implicit success or a new intermediate status.
An ordinary complete failure response may reject a request before a result stream
starts. If a result stream has started, terminate it with a failure end.

For both directions, text starts from an empty string and appends decoded string
data. JSON starts from a fresh `{}` and applies complete RFC 6902 JSON Patch
values. Start transmits no initial content. Arrays replace, null object members
delete, and non-object values replace the target, exactly as for messages. The
request and response have independent assembly state and may use different types.

**Provisional examples:** each line below is a separate envelope. Newlines group
them for reading; this does not introduce a batch or JSON-lines transport.
Resource URIs and schemas are illustrative application resources.

Text `set` request, from requester to responder:

```jsonl
{"id":"c1","method":"set","uri":"/drafts/d1","type":"text","stream":"start"}
{"id":"c1","method":"set","stream":"data","resource":"Hello "}
{"id":"c1","method":"set","stream":"data","resource":"world!"}
{"id":"c1","method":"set","stream":"end"}
```

The responder assembles `Hello world!`, validates and processes the request, then
returns a text response, from responder to requester:

```jsonl
{"id":"c1","method":"set","type":"text","stream":"start"}
{"id":"c1","method":"set","stream":"data","resource":"Saved "}
{"id":"c1","method":"set","stream":"data","resource":"draft d1."}
{"id":"c1","method":"set","stream":"end","status":"success"}
```

JSON `set` request, from requester to responder:

```jsonl
{"id":"c2","method":"set","uri":"/preferences","type":"json","stream":"start"}
{"id":"c2","method":"set","stream":"data","resource":[{"op":"add","path":"/locale","value":"en"}]}
{"id":"c2","method":"set","stream":"data","resource":[{"op":"add","path":"/notifications","value":{"email":true}}]}
{"id":"c2","method":"set","stream":"end"}
```

The assembled request resource is
`{"locale":"en","notifications":{"email":true}}`. After processing, a JSON
response streams independently:

```jsonl
{"id":"c2","method":"set","type":"json","stream":"start"}
{"id":"c2","method":"set","stream":"data","resource":[{"op":"add","path":"/saved","value":true}]}
{"id":"c2","method":"set","stream":"data","resource":[{"op":"add","path":"/preferences","value":{"locale":"en","notifications":{"email":true}}}]}
{"id":"c2","method":"set","stream":"end","status":"success"}
```

An intermediate `saved` field is provisional response data; it does not replace
terminal status or the product's requirement for durable action evidence.

**Correlation rules proposed for this grammar:**

- All streamed commands require an ID. A request start includes `uri`; a response
  start omits it and must match an outstanding local request by peer/routing
  context, ID, and method. Start/end without resource are still commands.
- Data/end inherit request-versus-response role from active assembly in that
  direction; `uri` is not repeated. Reject an unknown stream, duplicate start,
  mismatched method, conflicting reuse of an active ID, or data after termination.
  Active command IDs must be unambiguous across both directions in that routing
  context. Do not infer a role merely from absence of `status`.
- Finish request assembly before executing the operation or producing a successful
  result. Final schema validation, authorization, policy, and required approvals
  still apply. A `set` fragment never mutates the addressed resource by itself.
  Stream assembly is also distinct from the operation semantics of `method: "merge"`.
- On early rejection, prevent execution and discard further input for that rejected
  request under bounded cleanup rules. Disconnect before request end does not
  produce a complete invocation. Disconnect after submission but before response
  end leaves the caller's outcome unconfirmed; it does not prove non-execution.
- Request end is not a receipt, and commands do not join message watermarks.
  No message `rev` or public chunk sequence is added. Retry idempotency, requester
  cancellation/abort signaling, cleanup limits, and exact errors remain review items.

**Provider boundary:** JSON Patch can assign literal null-valued members and
update arrays incrementally. Provider argument text fragments are not operation
arrays: the adapter must assemble/validate and translate them, or send a complete
command. No automatic conversion of arbitrary provider fragments is claimed.
These constraints apply symmetrically to requests and responses.

### 7.1 Feature support and implementation limits

Capability declaration tells a sender whether its peer supports an optional
behavior such as JSON streaming. An alias only names a type; it does not declare
support. **Selected direction, 2026-10-07:** use the optional session `negotiating`
step for this when needed by a protocol implementation. The project's profile
will establish using support conventions without this exchange or another round
trip. The earlier optional-discovery-command suggestion is superseded for this
capability exchange; alias registration remains a separate command.

Proposed grammar, not yet selected: a `contentCapabilities` array with values
such as `text-streaming` and `json-streaming` on negotiating session envelopes.
Define whether these advertise receive support, how both directions are agreed,
and what omission means before freezing this extension. Recommended interpretation
is receive support per endpoint; sending support must not imply receiving support.
An omitted declaration must not mean universal support. A convention-based
profile must document the supported set; text streaming and optional JSON
streaming are separate capabilities. This review does not newly mandate JSON
streaming implementation, which may still be deferred.

**2026-10-08:** capability/profile support must distinguish receiving streamed
command requests from receiving streamed command responses, and text from JSON.
Message-streaming support alone does not imply either command direction. Exact
capability names remain open; no new mandatory project handshake is introduced.

Capability support does not install renderers, replace type/schema validation,
or authorize content/actions. Exact unsupported-stream rejection timing remains
under review. Capability extension names/values must also fit the strict grammar
once selected; current field names are illustrative.

**Selected 2026-10-07:** content sizes, buffer capacities, rate limits, and
backpressure mechanisms are left to implementation/profile design. This draft
does not add credit/window frames or numeric limits. Implementations must still
meet the repository's bounded-work and security requirements. Rejecting or
closing for overload cannot be reported as successful receipt. Exact peer-visible
error codes remain part of the error contract, not a queue-algorithm requirement.

### 7.2 Envelope discrimination and strictness

**Selected direction, 2026-10-07:** retain LIME's field-based identification; do
not introduce a separate envelope-kind field. The inspected
[Go implementation](https://github.com/takenet/lime-go/blob/master/envelope.go)
uses `method` with `uri` for requests, `method` with `status` for responses,
`event` for notifications, `content` for messages, and `state` for sessions.

**Updated 2026-10-08:** `stream` is shared by messages and commands. Classify by
the envelope's top-level fields before interpreting its payload. Proposed 2.0
discrimination, incorporating the selected command-streaming direction:

| Envelope family | Identifying fields |
| --- | --- |
| Session | `state` |
| Notification | `event` |
| Command | `method`, with or without `stream`; section 7.0 determines request/response role |
| Message | `content` or `stream`, with no `method`, `event`, or `state` |

`method` plus `stream` identifies a streamed command, never a message. Validate
that command even if its method value is invalid; never fall back to message
parsing. A message may contain both `stream` and `content`. Start/end need no
payload to be recognized. A command with `content`, a message with `resource`,
or any combination of competing `method`/`event`/`state` families is invalid.
Payload properties named `method`, `stream`, or `state` do not discriminate the
outer envelope. JSON versus text affects assembly, not envelope classification.
Field presence, not truthiness, matters: empty text and JSON null payloads
must not become unidentifiable. Then validate the selected envelope's fields,
values, and lifecycle. `type` remains shared content metadata, not a discriminator.

The user prefers strict handling; these detailed rules remain recommendations:

- Reject missing or competing family discriminators, invalid enum values,
  duplicate JSON keys, and fields not defined for that envelope/version or an
  explicitly supported extension. Do not select a family by precedence when
  several match.
- Reject unknown aliases and unsupported content types without interpreting them
  as text or generic JSON. A known alias alone does not grant schema support.
- Validate `content`/`resource` against their own type contracts; a generic `json`
  object may have arbitrary keys. Existing `metadata` can carry application
  context without changing core behavior; strictness is not a ban on those keys.
- Prefer a correlated message `failed` or command failure for a recognized,
  unsupported operation; use session failure/close when framing is unusable.
  Error codes and exact recovery/closure rules still need confirmation.

Strict decoding is not uniformly inherited from the Go library. Its
[message decoder](https://github.com/takenet/lime-go/blob/master/message.go)
uses ordinary `json.Unmarshal`, and the envelope classifier picks matches in
precedence order. The published [LIME message schema](https://limeprotocol.org/#message)
does forbid extra envelope properties. The new specification must distinguish
that stated contract from permissive implementation behavior.

## 8. Attachments and implementation boundaries

Recorded audio input follows the same out-of-band upload model as a PDF. LIME
carries the attachment reference and associated metadata/state. This voice use
case does not select live audio transport.

The proposed general service policy uses LIME for small conversation operations
and separate transfers for media/bulk data. HTTP transfer authorization and object
readiness are separate from session authentication and message receipt. An upload
claim alone does not prove a file is verified or usable.

Provider streams are inputs to a server adapter, not public protocol envelopes.
One model response can produce several messages. Provider IDs, counters, tool
arguments, and completion events do not bypass public assembly and validation.
The model-output framing strategy remains independent of this wire draft.

## 9. Application persistence and recovery

The earlier product/server direction is to reserve a message position and persist an
incomplete message when streaming begins, checkpoint accumulated content, and
persist the completed content before sending end. Store whole-message state;
do not require individual transmission chunks as history records. These are
product implementation choices, not requirements to use a particular database or
recovery mechanism in every protocol implementation.

Ordinary recovered history hides incomplete content. Abandoned positions need
tombstone or equivalent synchronization information. Recovery restores content,
not a typing animation. Partial-state retention, crash recovery, terminal skips,
late-producer fencing, snapshots, client cache loss, and database restoration
belong to the service's persistence design, not core session establishment.

The protocol does not cache transmission chunks or prescribe how a client cleans
up/displays an interrupted stream. Without end, however, that stream is not a
successfully completed message and cannot be acknowledged as such. A new session
does not resume its assembly automatically. The sender's unacknowledged-message
buffer is separate from temporary receiver assembly and durable product history.

These storage directions do not replace durable domain/audit events or authorize
a mutable-message-only source of truth. Preserve tenant isolation, idempotency,
authorization, provenance, and action execution evidence.

## 10. Review items before a stable wire release

Items 1–5 have been discussed on **2026-10-07**. Implementation details should not
block a core protocol release. Selected choices and remaining grammar questions
are distinguished below; item 6 records the **2026-10-08** command-streaming review:

| Original item | Disposition |
| --- | --- |
| 1. Identity and ordering | Application-defined IDs; UUIDs recommended. Complete messages may omit `id`; streaming/correlated progress requires it. Revision allocation/presentation, thread binding, and authorship remain application concerns. Edits retain the displayed message position. |
| 2. Recovery and failure | Retain the LIME notification envelope, adjusted by the selected revisions, events, and notification scopes. Each sender buffers and retries unacknowledged messages. Buffer sizing and interrupted-stream cleanup are implementation choices; no chunk cache is required. |
| 3. Session profile | HTTP authentication is outside LIME. Retain fallback authentication. Add `version: 2` to `new`; omitted/unsupported versions produce session failure and close. Reconnect always creates a new session ID; no session resume. |
| 4. Extensions and limits | Initial alias table selected from LIME's content-types page plus `json`. Registration remains a separate session command; its URI/payload/authority are open. General protocol capability exchange may use optional `negotiating`; the project skips it through conventions. Capability field/values/direction/defaults remain open. Numeric bounds and backpressure mechanisms belong to implementation/profile design. |
| 5. Grammar | No `stream` means complete message; `id` is optional in that form. Positive integer revisions within the stated range and fresh `{}` JSON assembly at start are selected. Retain field-based detection with the necessary `stream` addition; detailed strict-validation/error rules remain recommendations. |
| 6. Command streaming (2026-10-08) | Requests and responses both support text and JSON streaming at protocol level; runtime may implement either direction independently. Reuse `stream` with `resource`, and retain `method` for discrimination. Section 7.0 proposes correlation/lifecycle details; requester abort/cancellation, retry, and capability grammar remain open. |

Two delivery details remain recommendations rather than selected rules: keeping
the same `(id, rev)` on retry, and how cumulative progress treats an earlier failed
or incomplete message. The latter asks whether a receipt for B can clear A when A
never completed; it does not require a core snapshot or tombstone service.
ID-less fire-and-forget traffic also needs an explicit receipt-buffer boundary;
the recommendation in section 4 excludes it from cumulative acknowledgement.
Snapshot/live handoff simply means switching from stored history to new arrivals
without gaps or duplicates; that mechanism belongs to application history APIs.

Custom alias registration is already assigned to commands; these review items
must not reintroduce a mandatory alias-negotiation handshake. Optional features
must be discoverable by convention or extension, or fail predictably.

## 11. Repository authority and validation

This draft does not supersede the [product foundation](https://github.com/andrebires/fast-chat/blob/main/docs/product-foundation.md)
or [accepted ADRs](https://github.com/andrebires/fast-chat/blob/main/docs/adr/README.md). Before affected runtime work, reconcile:

| ADR | Reconciliation |
| --- | --- |
| [0003](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0003-conversation-event-stream.md) | Map public messages/revisions to authoritative durable events. |
| [0004](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0004-lime-over-websocket.md) | Review new streaming, session defaults, receipts, aliases, and HTTP transfer exceptions. |
| [0005](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0005-typed-rich-ui.md) | Supersede mandatory multipart while retaining validated, trusted UI. |
| [0006](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0006-operational-data-backbone.md) | Preserve PostgreSQL authority, transactional outbox, and acknowledged durability. |

Authentication and attachment behavior must retain
[ADR 0008](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0008-identity-and-resume.md) and
[ADR 0016](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0016-security-privacy-and-abuse.md). Recorded-audio intent is not
authorization to expand the current product milestone. Business actions retain
the [policy/action boundary](https://github.com/andrebires/fast-chat/blob/main/docs/adr/0009-orchestrator-action-boundary.md).

Document validation covers example JSON, local links, whitespace, and repository
verification. It is not protocol interoperability testing. Future contract tests
must cover duplicates, default revisions, interrupted streams, RFC 6902 operations,
unsupported features, cumulative gaps, replacement replay, and tenant boundaries.
Command-streaming contracts must additionally cover both request/response
directions with text/JSON, top-level discrimination, concurrent streams and ID
collisions, rejection before execution, failure after partial results, null-member
semantics, and disconnects before/after submission.
Performance validation must include session round trips, time to first permitted
content, message completion, payload/CPU cost, and recovery under realistic load.
