## LIME2-T04 — Adopt RFC 6902 structured streaming

- Status: Completed
- Authority: user selection, 2026-10-08; superseding ADR 0002.
- Outcome: all six JSON Patch operations, efficient incremental array updates,
  literal null, strict pointers/indices, bounded document/depth/operations/copy work,
  no partial completion on rejection, and compatible Go/browser contract vectors.
- Acceptance: shared RFC fixtures, invalid/ownership/replay contracts, demo updated,
  full formatting/vet/race/build/frontend verification and >=90% changed coverage;
  array-append benchmarks and browser verification.
- Scope: message assembly and the existing demo. Command streaming remains outside
  this implementation; no new negotiation grammar or persistence.

# Implementation backlog

- Evidence: full verification passes with 576/607 changed executable lines (94.89%); Go package coverage 97.2%, demo 93.2%; browser 99.59% lines / 100% functions / 94.27% branches. All 74 shared vectors pass; fuzzing completed 299,106 cases. Two live browser clients verified append, removal, end and final receipt with no console errors; [evidence](docs/demo-json-patch.png). Array benchmarks are recorded in docs/performance.md.

## LIME2-T03 — Address PR review and merge verified implementation

- Status: Verified; delivery and merge state tracked in [PR #1](https://github.com/andrebires/lime-go/pull/1).
- Authority: user request to handle PR #1 comments and merge when green.
- Outcome: aliases require usable schemas; broadcast retry state remains tied to
  every original recipient; failed fan-out cannot leave live orphan streams.
- Acceptance: regression tests cover all three review findings, partial transport
  failures and cumulative broadcast receipts; verification and CI pass, review
  threads are addressed, and the verified head is merged into master.
- Excludes: new wire events, reconnect/resume, persistent retry storage.
- Evidence: local formatting/vet/build/race/integration/frontend verification
  passed; full PR changed-line coverage 2193/2276 (96.35%). Browser client
  coverage is 100% lines/functions and 96.20% branches. Regressions cover schema
  registration, outstanding broadcast recipients, frozen routing, disconnects,
  start/data/end fan-out failures, capacity, cancellation and reordered status
  replies. Three live clients verified selective whole-message retry and final
  cumulative receipt; [screenshot](docs/demo-review-fixes.jpg).

## LIME2-T02 — Demonstrate cumulative notification scopes

- Status: Done
- Authority: user request 2026-10-07; notification scopes in ADR 0001 and draft §5.2.
- Outcome: selectable message/session receipts and message/thread read notifications
  in the browser demo, with automatic/manual receipts and visible scope feedback.
- Acceptance: cumulative markers use delivery order, stop at incomplete gaps,
  preserve exact revisions, stay within recipient/thread boundaries, and work with
  multiple senders. Tests, race/build/static checks, >=90% diff coverage and live
  browser verification pass.
- Excludes: new wire events/scopes, durable storage, session resume.
- Evidence: `./scripts/verify.sh HEAD` passed with 134/134 (100%) changed
  executable lines covered. Full PR gate: 1967/2053 (95.81%). Browser contract
  coverage: 100% lines/functions, 96.20% branches. WebSocket integration tests
  verify scoped relay and gap rejection. Three live browser clients verified
  session receipts across senders/threads, stream gaps and isolated thread reads;
  [screenshot](docs/demo-scoped-notifications.jpg).

## LIME2-T01 — JSON protocol stack and live multi-client demo

- Status: Done
- Authority: user request 2026-10-07; [ADR 0001](docs/adr/0001-lime2-profile.md).
- Outcome: a bounded, authenticated LIME 2.0 stack with complete and streamed
  messages, notifications, commands, session aliases, retry tracking, and a demo.
- Acceptance: strict wire conformance; version/authentication rejection; text and
  RFC 7396 assembly; revision/scope/gap/duplicate handling; bounded slow peers;
  reconnect creates fresh state; runnable server and clients with live typing;
  race/static/build checks and >=90% changed executable line coverage in CI;
  measured codec/allocation benchmarks against the existing library.
- Excludes: fast-chat integration, PostgreSQL/history/resume, production business
  authorization, capability negotiation, binary encodings, LIME 1 wire compatibility.
- Evidence: `./scripts/verify.sh origin/master` passed formatting, vet, build,
  race/integration and frontend contract checks; changed executable coverage
  1871/1957 (95.61%). Browser-client coverage: 100% lines/functions, 96.30% branches.
  Codec fuzzing completed 786,454 cases without failures. Three connected browser
  clients verified live text broadcast, JSON patches and received/consumed
  notifications; [screenshot](docs/demo-browser.jpg). Allocation/codec baseline
  comparisons are recorded in [performance evidence](docs/performance.md).

## LIME2-T05 — Stream command requests and responses

- Status: Completed
- Authority: user request, 2026-10-08; draft sections 7.0/7.2, ADR 0003.
- Outcome: strict text/JSON Patch command streams in both directions, independent correlated assembly and terminal status, complete command interoperability.
- Acceptance: no application invocation before request end; peer/method/direction isolation; bounded exchanges, resource/work limits and caller-controlled timeout/disconnect cleanup; no message receipts/retries; shared fixtures, WebSocket integration, race/vet/build and >=90% changed coverage.
- Excludes: new capability grammar, automatic retries, cancellation wire fields, durable execution claims and fast-chat runtime integration.
- Evidence: full verify passes formatting/vet/race/integration/build/frontend and 245/257 changed executable lines (95.33%); root Go coverage 97.2%, demo 92.9%. All 39 shared command vectors pass; WebSocket tests cover invocation boundaries, result terminal status, interruption and demo alias mutation only after end. Command-stream benchmark evidence is committed.
