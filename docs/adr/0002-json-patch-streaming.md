# ADR 0002: RFC 6902 structured streaming

- Status: Accepted
- Date: 2026-10-08
- Task: LIME2-T04
- Authority: explicit user selection and request to update lime-go.
- Supersedes: [ADR 0001](0001-lime2-profile.md) for JSON stream assembly only;
  every other profile decision in ADR 0001 remains in force.

## Context and decision

Growing arrays require whole-array replacement under RFC 7396, and null object
members cannot be assigned directly. Adopt RFC 6902 JSON Patch for structured
stream contributions, matching AG-UI structured deltas. This changes the draft
wire semantics and the helper API; there is no automatic Merge Patch detection.
Old and new LIME 2 draft peers must be upgraded together.

## Detailed design and invariants

Start initializes fresh {} per revision. Data is a complete operation array.
Support add/remove/replace/move/copy/test, RFC 6901 pointers, array append /-, and
root replacement. End requires a JSON value; root removal must be followed by
root add before end. The final MIME type describes the assembled document.
Invalid batches abandon the provisional stream before progress/completion/receipt.
Whole-message retry and previous completed revisions retain their semantics.

The assembler owns a parsed tree, caches serialized sizes, updates ancestors on
mutation, and serializes only at end. Array append is amortized constant time at
fixed nesting depth. Bounds apply to each contribution, assembled size, operation
count (Limits.Entries per batch), depth 64, and bytes cloned by copy/move per batch
(no more than ContentBytes). Exact numeric literals are retained; test compares
numeric values without binary float rounding or exponent-sized allocations.

## Rejected alternatives and consequences

Dual patch formats add ambiguous interpretation and interoperability branches.
Raw JSON token fragments require a distinct parsing contract. Complete JSON stays
available; patches do not authorize commands, UI rendering, or business actions.
The exported JSONPatch replaces MergePatch; existing callers migrate explicitly.

## Failure, security, privacy and observability

Pointers traverse only document members, never Go/JavaScript prototypes. Copy
owns mutable subtrees. Invalid indices, missing parents, failed tests, descendant
moves and limits reject the stream. Existing error callbacks and active/pending
counts report failures; no payload or credential logging is added.

## Validation

Shared RFC examples and additional array/null/pointer/root/error vectors run in Go
and JS, with transport replay tests, bounds/ownership/depth tests, race/vet/build,
>=90% changed coverage, browser demo verification, and array-append benchmarks.
