# LIME 2.0 for Go

An experimental, non-binary LIME 2.0 stack based on the
[2026-10-08 v0.1 draft](docs/lime-2-v0.1-draft.md) and the
[approved JSON Patch profile](docs/adr/0002-json-patch-streaming.md), with
[command streaming](docs/adr/0003-command-streaming.md).
The wire remains readable JSON over WebSocket text messages (`lime` subprotocol).

This is a **breaking rewrite**. Import `github.com/andrebires/lime-go/v2`.
The old factories, builders, channel hierarchy, TCP/in-process transports and
legacy demos are removed. There is no automatic downgrade to LIME 1, session
resume, or production storage. The profile is a concrete implementation of a
review draft, not an official frozen LIME standard.

## Run the demo

Requires Go 1.25.5+. No npm install or frontend build is needed.

```sh
go run ./examples/lime2-demo
```

Open **http://127.0.0.1:8080** in two or more tabs. Connect each client, refresh
peers, choose a recipient or broadcast, and type. Characters arrive before you
press **Finish / send**. Live text is append-only during a stream; finish before
editing previous text. The wire viewer shows start/data/end and correlated receipts.

The JSON button shows RFC 6902 array append and explicit member removal with a deliberate 750 ms interval. You can register the session
alias `note`, mark completed messages read, retry whole unreceived messages,
disconnect and establish a new session. The tests additionally cover default and
non-default revisions, cumulative receipt/read gaps, failed entries, unsupported
versions, spoofed identities, binary frames, overload and interrupted streams.

Choose **Receipt scope: Session prefix** to send `received` watermarks across
threads, or keep individual message receipts. Turn off **Automatic receipts**
and use **Send received notifications** to inspect pending messages and retries.
Choose **Read scope: Thread prefix**, enter the target **Thread**, and press
**Mark completed messages read** to send a cumulative `consumed` watermark for
that thread. Other threads remain outside that read notification. Cumulative
progress uses delivery order and stops before unfinished streams; after finishing
the stream, send receipts/read progress again. A session containing several
original senders sends each origin its latest covered marker. Notification status
and the wire viewer display the scope, exact message ID and revision.
See [scoped notification browser evidence](docs/demo-scoped-notifications.jpg).

Broadcast retry state remains pending until every original recipient acknowledges.
The demo command `get /messages/delivery` with JSON resource `{"id":"m1","rev":1}`
reports outstanding recipient nodes; `set` on the same resource retries their
retained complete revisions. The Retry button uses that command, so a new peer
never joins an old broadcast and acknowledged peers receive no replay. Missing
recipient sessions remain outstanding; reconnect abandons session-local state.
The sender retains at most 256 delivery records and never evicts an unacknowledged
record. Retrying one revision copies only that revision's payload.
See [broadcast retry browser evidence](docs/demo-review-fixes.jpg).

If a fan-out fails after stream start, the demo closes only recipient sessions
that still hold the affected partial stream. This also applies when the sender
disconnects or sends invalid stream data. LIME has no stream-abort signal; closing
the session makes interruption visible and clears assembly without issuing a
false end or receipt. Recipients that already completed end stay connected.

The demo listens on loopback only (override with `-addr 127.0.0.1:8087`). It uses
short-lived one-use anonymous capabilities with LIME's existing plain/base64
credential convention. Credentials do not enter URLs or the wire log. Every tab
gets a server-bound random identity. **Messages are ephemeral**; production
applications must commit successfully before sending end or acknowledging receipt.

## Use the stack

```go
registry, err := lime.NewRegistry(lime.Limits{})
if err != nil { return err }
assembler, err := lime.NewAssembler(registry, lime.Limits{})
if err != nil { return err }
assembler.Commit = func(message lime.Envelope) error {
    return persistCompleteMessage(message) // application-owned transaction
}

// c is a *lime.Conn created with Dial or Upgrade and an established session.
for {
    envelope, err := c.Receive(ctx)
    if err != nil { return err }
    kind, err := envelope.Kind()
    if err != nil { return err }
    if kind != lime.Message { continue } // dispatch sessions/commands/notifications
    result, err := assembler.Apply(envelope)
    if err != nil { return err } // send correlated failed; discard the stream
    if result.Complete && result.Message.ID != "" {
        // For Duplicate, reissue receipt without redisplaying/recommitting.
        err = c.Send(ctx, lime.Envelope{
            ID: result.Message.ID, Rev: result.Message.Rev,
            To: result.Message.From, Event: "received",
        })
        if err != nil { return err }
    }
}
```

`Envelope.Content` and `Resource` are `json.RawMessage`; nil means absent and
`[]byte("null")` means JSON null. `Rev == 0` encodes omission, whose wire meaning
is revision 1. Start declares type; data carries a contribution; end carries
neither. `Result.Message` on data is the contribution with resolved type/routing;
complete assembled content is returned only at end. JSON numbers retain their
literal precision. Structured data contributions are RFC 6902 operation arrays;
all six operations and RFC 6901 pointers are supported. Each revision starts at
{}; root removal requires replacement before end. Invalid batches discard the
provisional stream. Whole-message retries never replay array-append patches.
The exported `JSONPatch` replaces the former `MergePatch` helper. This changes
the draft wire format: upgrade structured-stream peers together. Literal null
is a value; deletion uses `remove`. Operation count is bounded by `Limits.Entries`
per batch; document/contribution size and copy work by `ContentBytes`.
Serialized JSON inside a text string stays text.

`Decode` owns returned bytes and rejects ambiguous families, duplicate keys,
unknown fields, invalid UTF-8, wrong lifecycle fields and illegal event/scope pairs.
`Append` writes into a caller-owned reusable buffer; raw values are validated.
`Conn` serializes reads/writes with cancelable ownership gates. Cancellation during
I/O closes the socket; canceled queued operations leave the owner alone. Writes
have a ten-second upper deadline. Callers own session shutdown and dispatch.

Use `AcceptSession` with a verified `TransportIdentity` from HTTP authentication,
or an `Authenticate` callback for the existing plain scheme. Configure `LocalNode`
when the server should advertise its routing identity; it is returned as the
client session's `Remote` node. Resolve omitted from/to fields using verified
session context before applying delivery tracking, and track resolved MIME types. `EstablishSession`
sends `version: 2`. Production use requires TLS, verified authorized scope and
per-operation authorization. Peer `from` and `pp` fields never override the bound
identity. See the demo for routing and the profile for reason codes.

A registry owns aliases and schemas for one session. Built-in aliases match the
draft. Only text/plain and application/json have default content acceptance;
recognizing a vendor alias does not install its schema. `Registry.Support` installs
an explicit validator for a supported canonical custom/vendor type. Unsupported
streaming types are rejected at start. `AliasCommand` implements get/set
`/protocol/aliases`; registration is atomic, bounded, additive and session-local.
Trusted rendering and business validation remain application concerns.

`Tracker` owns delivery order for one peer/direction. Resolve MIME aliases before tracking. Reserve with `Track(resolvedStart)`
and replace that entry with `Track(assembledCompleteMessage)` before releasing
end. `Pending` returns owned complete-message snapshots for bounded caller-scheduled
retries, preserving `(id, rev)` without storing chunks. Apply notifications with
`Apply`. Message received clears only its entry; session received requires a
completed contiguous prefix. Failed/consumed never release receipt payloads.
Use explicit `Resolve` only after both peers agree to resolve a failed entry.
`MarkRead` plus `ReadNotification` prevents local thread read watermarks crossing
unread gaps. ID-less messages are outside this ledger. No reconnect state is
inherited automatically; unknown/expired markers fail.

Defaults are 64 KiB frames, 1 MiB assembled content, 8 MiB pending retry payloads,
32 active streams, 256 retained delivery/dedup markers, 256 contributions per
stream, 32 custom aliases/types, and JSON depth 64. Completed deduplication retains
SHA-256 digests and routing, rather than whole payloads. Received/resolved retry
payloads are released immediately. Configure `Limits` at startup for the service's
memory budget; these are finite implementation limits, not standardized wire sizes.

## Verify and measure

```sh
./scripts/verify.sh origin/master
make bench
```

Verification runs formatting, whitespace, vet, builds, race/integration tests,
frontend contract tests, and **90% coverage of changed executable Go/JavaScript
lines**. Node 22+ is needed only for verification. GitHub Actions enforces the
same gate and does not automatically tag/release this experimental protocol.

See [benchmark evidence](docs/performance.md), [the implementation task](BACKLOG.md),
and [browser verification](docs/demo-browser.jpg). Microbenchmarks are codec
measurements, not production end-to-end latency or GC pause guarantees.

## Command streams

The profile supports text and RFC 6902 JSON Patch requests and responses using
id/method on every frame and resource on data. Request start includes URI/type;
response start includes type and omits URI/status. Request end submits the input;
response end declares success/failure, with reason for failure. Commands never
enter message receipts, revisions, deduplication or retry buffers.

```go
commands, err := lime.NewCommandAssembler(registry, lime.Limits{})
if err != nil { return err }
defer commands.Reset() // one object per endpoint/session, used serially
request := lime.Envelope{ID:"c", From:local, To:remote, Method:"set",
    URI:"/preferences", Type:"json", Stream:"start"}
if _, err = commands.Apply(request, lime.OutgoingCommand); err != nil { return err }
if err = c.Send(ctx, request); err != nil {
    commands.Discard(request, lime.OutgoingCommand)
    return err
}
// Send data/end through Apply(OutgoingCommand) before c.Send, then receive:
envelope, err := c.Receive(ctx)
if err != nil {
    commands.Discard(request, lime.OutgoingCommand)
    return err // timeout after submission leaves execution unconfirmed
}
result, err := commands.Apply(envelope, lime.IncomingCommand)
if err != nil { return err }
if result.Complete && result.Response { return handleResult(result.Command) }
```

Bind omitted from/to fields to authenticated session routing before Apply. A
response stream must match a submitted outgoing request by peer, ID and method.
A received request becomes invokable only when Complete is true and Response is
false. Apply outgoing responses as well to release the remote command ID at end.
A complete ordinary failure can reject incomplete input; once a response stream
starts, use failure end. Failed result streams return status/reason and omit
partial type/resource. Complete requests and responses remain independently usable.

Limits apply to active exchanges (Entries), active payload streams (Streams),
contributions/operations (Entries), resource bytes, JSON depth and copy work.
Active reports reserved exchanges; even completed incoming requests await a
terminal outgoing response. Discard releases an exchange on rejection, timeout
or failed send; Reset abandons all state on disconnect. Callers own absolute
context deadlines and receive-loop dispatch; progress must not renew deadlines.
No command goroutines or automatic retries are created. Use fresh IDs per
invocation to avoid delayed-frame ambiguity after completed-ID reuse. Data results
borrow the input contribution bytes, while complete resources own their bytes.

The existing demo server now assembles streamed requests before alias/delivery/
peer handlers execute and returns complete responses. Its browser controls use
ordinary complete commands; bidirectional response streams are verified through
the library's WebSocket and shared-fixture tests. Capability/abort wire fields
remain undefined; support in both directions is a profile convention.
