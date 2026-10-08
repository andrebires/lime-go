# ADR 0003: Command request and response streaming

- Status: Accepted
- Date: 2026-10-08
- Task: LIME2-T05
- Authority: explicit user implementation request for draft sections 7.0 and 7.2.
- Extends: ADR 0001 command handling and ADR 0002 structured assembly; their other decisions remain in force.

## Context and decision

Implement the supplied command grammar in both directions by profile convention.
Every contribution carries id and method; resource belongs only on data, type on
start, URI only on request start, status only on response end. Complete commands
remain valid. Missing status never determines a streamed command's role.

## Design and invariants

CommandAssembler owns both directions for one endpoint/session. Apply outgoing
requests before sending; incoming response streams must match peer, ID, method
and a submitted request. Track remote requests until the outgoing response ends.
Reject conflicting active IDs, duplicate starts, changed routing/method, unknown
streams and post-termination data. Request and response use independent text or
fresh-object JSON Patch state. Only a completed request may be invoked. Success
requires response end; failure end discards provisional response data. An ordinary
failure may reject an unfinished request; following contributions are rejected.
Commands never enter message receipt/watermark, revision, dedup or retry tracking.

Limits bound active exchanges (Entries), active streams (Streams), contribution
count/operations (Entries), content size, depth and copy work. State is serial and
owned. Applications own absolute context deadlines and call Discard on timeout,
rejection or failed send and Reset on disconnect. Timeouts after submission leave
the outcome unconfirmed. Reuse of completed command IDs is caller policy; fresh
session IDs per invocation avoid delayed-frame ambiguity.

## Alternatives and consequences

No per-chunk goroutines, implicit side effects, automatic command retries,
message receipts for commands or speculative negotiation/cancellation fields.
The demo assembles requests before existing command handlers execute. Applications
retain schema validation, authorization, policy, approvals and execution evidence.

## Failure, security, observability and validation

Wrong peers cannot finish another exchange. Invalid batches discard provisional
state; partial failure resources are never promoted. Active counts and errors
expose saturation and cleanup without logging resources. Shared wire fixtures,
text/JSON and complete-form contracts, failure/timeout/disconnect tests, WebSocket
integration, race/vet/build and at least 90 percent changed coverage verify behavior.
