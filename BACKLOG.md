# Implementation backlog

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
