# Implementation backlog

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
