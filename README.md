# LIME 2.0 for Go

An experimental, non-binary LIME 2.0 stack based on the
[2026-10-07 v0.1 draft](docs/lime-2-v0.1-draft.md) and the
[approved implementation profile](docs/adr/0001-lime2-profile.md).
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

The JSON button shows RFC 7396 recursive merging, array replacement, and null
member deletion with a deliberate 750 ms interval. You can register the session
alias `note`, mark completed messages read, retry whole unreceived messages,
disconnect and establish a new session. The tests additionally cover default and
non-default revisions, cumulative receipt/read gaps, failed entries, unsupported
versions, spoofed identities, binary frames, overload and interrupted streams.

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
literal precision. Serialized JSON inside a text string stays text.

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
