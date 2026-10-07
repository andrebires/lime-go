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

These results do not establish throughput for large JSON Merge Patches, network
fanout, production persistence, battery consumption, or GC tail pauses. Patch
application necessarily visits/serializes affected assembled JSON. Services should
benchmark representative payloads, batching and peer counts under their own limits.
