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

Rule: every `valid/` file passes and every `invalid/` file fails.

## Semantics

`null` for `inputs.brake` / `inputs.throttle` means "no reading", never `0`.
`inputs.steering` is always `null` in v1.

JSON cannot carry NaN or Inf, so those cases are covered by Go encoder tests
in Phase 1, not by fixtures.
