# gt7.inputs.v1 contract fixtures

Spec: `docs/adr/0002-browser-input-contract-v1.md` (authoritative).

## Ownership

The collector owns the protocol. The trainer copies a tagged snapshot of this
directory; there is no code dependency between repositories.

## Layout

- `schema.json` - JSON Schema (draft 2020-12), `oneOf` hello / sample / status.
  Unknown extra properties are allowed (v1 consumers ignore unknown fields);
  types of known fields are strict.
- `valid/*.json` - messages that MUST pass the schema.
- `invalid/*.json` - messages that MUST fail the schema, one defect each.
  These are emitter conformance cases: the collector must never send them.
  They are not a list of messages a consumer must reject. Consumers apply the
  ADR 0002 rule 6 tolerance rules instead of hard-failing on full schema
  validation: for example, ignore a message with an unknown `type`
  (`unknown.type.json`), and do not fail the connection on a `hello` whose
  steering capability differs from today's (`hello.steering-available.json`);
  whether to use steering is the consumer's decision.
- `transcripts/*.jsonl` - whole-connection transcripts recorded from real hub
  runs (`hub_test.go`, `TestTranscripts`), one compact message per line in
  send order. Every line passes the schema. Regenerate with
  `go test ./browserstream/ -run TestTranscripts -update` and review the diff.
  - `hello-then-samples` - source already live at connect: `hello` then
    samples, no status.
  - `waiting-first-frame` - `hello` (waiting), then `source_live`/`first_frame`
    before the first sample.
  - `drop-arithmetic` - 4-slot queue, writer paused while 12 frames arrive;
    `droppedSincePrevious` equals the sequence gap.
  - `stale-resume` - source goes stale with a full backlog: `source_stale`
    goes ahead of queued samples, which are discarded; `source_resumed`
    precedes the next sample, whose `droppedSincePrevious` counts both
    queue evictions and the discarded backlog.

Rule: every `valid/` file passes and every `invalid/` file fails.

## Semantics

`null` for `inputs.brake` / `inputs.throttle` means "no reading", never `0`.
`inputs.steering` is always `null` in v1.

JSON cannot carry NaN or Inf, so those cases are covered by Go encoder tests
in Phase 1, not by fixtures.
