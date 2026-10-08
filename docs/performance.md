# Performance evidence

Measured on 2026-10-07, Apple M5 Pro, macOS arm64, Go 1.26.3. Three repetitions;
medians below. Baseline is commit `463631a` in andrebires/lime-go, measured before
replacement with its actual Message/json.Marshal and Message/json.Unmarshal APIs.
Both fixtures encode/decode the same complete text message, ID m1, canonical
text/plain, content "Hello from Lime!". The 2.0 decoder additionally performs
strict duplicate/unknown-field/lifecycle checks.

| Path | Median ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Legacy encode via json.Marshal | 589.1 | 465 | 9 |
| 2.0 encode with a fresh buffer | 147.3 | 120 | 4 |
| 2.0 append into reused buffer | 105.3 | 0 | 0 |
| Legacy decode | 910.9 | 1184 | 19 |
| 2.0 strict decode | 663.1 | 656 | 11 |
| 2.0 assemble 100 five-character text contributions and complete | 14575 | 3050 | 18 |

Fresh-buffer encoding is about 4.0x as fast for this fixture. Reused-buffer
encoding is about 5.6x as fast and allocation-free. Strict decoding is about 1.37x
as fast, with 44.6% fewer allocated bytes and 42.1% fewer allocations. The streaming
row includes validation, decoded strings, assembly, final quoting and deduplication;
its allocations are per whole 100-contribution operation, not per contribution.

The implementation avoids document factory registries, intermediate raw envelopes,
repeated content marshalling, full accumulated-text snapshots on every chunk, temporary decoding strings for
ordinary unescaped UTF-8 text contributions, and
per-envelope I/O goroutines. It uses safe Go code rather than unsafe byte/string
aliasing. Completion deduplication stores digests, and receipt processing immediately
releases payload bytes. Pending whole-message payloads have a separate byte budget.

Reproduce current measurements:

```sh
go test -run '^$' -bench . -benchmem -count=3 .
```

For the baseline, check out commit 463631a in a separate directory and add:

```go
func BenchmarkBaselineEncodeText(b *testing.B) {
    m := Message{Envelope: Envelope{ID: "m1"},
        Type: MediaTypeTextPlain(), Content: TextDocument("Hello from Lime!")}
    b.ReportAllocs()
    for b.Loop() { if _, err := json.Marshal(&m); err != nil { b.Fatal(err) } }
}
func BenchmarkBaselineDecodeText(b *testing.B) {
    data := []byte(`{"id":"m1","type":"text/plain","content":"Hello from Lime!"}`)
    b.ReportAllocs()
    for b.Loop() {
        var m Message
        if err := json.Unmarshal(data, &m); err != nil { b.Fatal(err) }
    }
}
```

These results do not establish throughput for large JSON patches, network
fanout, production persistence, battery consumption, or GC tail pauses. Structured streams retain an owned tree and serialize it only at completion. Services should
benchmark representative payloads, batching and peer counts under their own limits.

## JSON Patch array append, 2026-10-08

Go 1.26.3, darwin/arm64, Apple M5 Pro; three repetitions, medians. Each row
parses and applies one small RFC 6902 batch per item and serializes once at end.
This measures the patch engine, not transport, schema validation or rendering.

| Items | Time per document | Allocated bytes | Allocations |
| --- | ---: | ---: | ---: |
| 100 | 0.162 ms | 287,842 | 4,730 |
| 1,000 | 1.645 ms | 2,858,467 | 47,043 |
| 10,000 | 17.135 ms | 28,700,213 | 470,167 |

The fixture grows approximately with item count and avoids reserializing prior
items at each append. Per-batch JSON decoding still allocates; these totals are
per complete document. Reproduce: `go test -run '^$' -bench BenchmarkJSONArrayAppend -benchmem -count=3 .`.
Raw output is in [json-patch-benchmark.txt](json-patch-benchmark.txt).

## Command streaming, 2026-10-08

BenchmarkCommandTextStream measures one 100-contribution request (500 decoded
characters), final assembly/schema validation, and a complete reply releasing
its exchange. Three Go benchmark repetitions on Apple M5 Pro, darwin/arm64,
Go 1.26.3: median 17,438 ns/document, 3,522 B and 18 allocations/document. Raw
output is in [command-stream-benchmark.txt](command-stream-benchmark.txt).
It includes no WebSocket I/O, application authorization or execution. Contributions
borrow decoded input bytes for progress; text is accumulated once and quoted at
end, so this fixture's allocation count is per document, not per chunk.
Reproduce with go test -run '^$' -bench '^BenchmarkCommandTextStream$' -benchmem -count=3 .

A separate live Node 24 lime-js / Go Conn / CommandAssembler smoke check over a
loopback WebSocket fixture with transport-established identity passed JSON Patch
array/null input, text input, independently streamed JSON responses, a probe
confirming zero handler invocations before request end, and terminal exchange
cleanup. This was a correctness check; its network latency was not benchmarked.
