# ADR 0002: Versioned minimal JSON browser contract `gt7.inputs.v1`

- Status: Proposed (Phase 0)
- Date: 2026-11-14
- Requirements: C-007..C-011, C-021, C-030

## Context

The canonical `TelemetryFrame` has 100+ fields, many GT7-specific and several
with unverified semantics. The browser trainer needs driver inputs plus enough
metadata to detect loss, staleness, replay and session boundaries. Browsers
should not need protobuf bindings.

## Decision

The collector owns the protocol. The browser implements it. The contract is
a JSON text-frame protocol negotiated as WebSocket subprotocol
`gt7.inputs.v1`, independent of the canonical protobuf `SchemaVersion`.

Fixtures and the JSON Schema live in `browserstream/testdata/v1/`
(`schema.json`, `valid/*.json`, `invalid/*.json`). Collector tests (Phase 1)
assert the encoder output matches `valid/` byte-for-byte after JSON
normalization and that every `invalid/` case fails schema validation. The
trainer copies a tagged snapshot of that directory; there is no code
dependency between repositories.

### Messages (server -> client only)

Every message has `type`, `protocol` (always `"gt7.inputs.v1"`) and
`sessionId`.

**`hello`**: first message on every connection, before any sample.

| Field | Type | Notes |
|---|---|---|
| `collectorVersion` | string | `main.version` (`dev` for local builds) |
| `canonicalSchemaVersion` | integer | `telemetry.SchemaVersion` (1) |
| `capabilities.brake` / `.throttle` | object | `{available:true, unit:"percent", min:0, max:100}` |
| `capabilities.steering` | object | `{available:false, reason:"driver_steering_input_unverified"}` |
| `status` | string | `waiting_for_source` \| `source_live` \| `source_stale` at connect time |

**`sample`**: one per canonical frame delivered to this client.

| Field | Type | Source / rule |
|---|---|---|
| `sequence` | string | canonical `Sequence` as a decimal string (uint64 exceeds 2^53) |
| `packageId` | integer | canonical `PackageId` (uint32, JS-safe) |
| `source` | string | `LIVE` \| `REPLAY` \| `UNSPECIFIED`, copied from canonical `Source`; never relabelled |
| `recvTimestamp` | string | canonical `RecvTimestamp`, RFC 3339, UTC, nanosecond precision, `Z` suffix |
| `sourceGap` | boolean | canonical `Gap` (GT7 PackageID discontinuity) |
| `droppedSincePrevious` | integer | samples discarded from **this client's** queue since the previous sample sent to this client |
| `inputs.brake` | number \| null | canonical `Brake` (0..100 percent, GT7 raw/2.55), no transform/smoothing |
| `inputs.throttle` | number \| null | canonical `Throttle`, same rules |
| `inputs.steering` | null | always `null` in v1 |

**`status`**: per-connection source state change.

| `state` | `reason` | When |
|---|---|---|
| `source_live` | `first_frame` | immediately before the first sample after `waiting_for_source` |
| `source_live` | `source_resumed` | immediately before the first sample after `source_stale` |
| `source_stale` | `no_accepted_frames_within_timeout` | hub declares the source stale |

### Source state ownership and ordering

- **One owner.** The hub holds the authoritative source state
  (`waiting_for_source` -> `source_live` <-> `source_stale`) plus a
  generation counter. `Publish` (receive goroutine) stores the last-frame
  monotonic time and last sequence and flips `waiting`/`stale` -> `live` with
  one CAS. A single hub monitor goroutine ticks every
  `--ws-source-timeout / 2` and flips `live` -> `stale` when no frame arrived
  within the timeout, recording `staleAtSequence` (the last published
  sequence). Stale detection latency is therefore 1x-1.5x the timeout. No
  per-frame timer resets; writers run no staleness timers.
- **Notification.** On each transition the monitor does a non-blocking send
  on every client's 1-slot `notify` channel (coalescing).
- **`hello` race.** A connection registers its client **first**, then
  snapshots the hub state for `hello`. That snapshot is the writer's baseline
  "last state sent". A frame published between registration and the
  snapshot is already queued and is sent after `hello`.
- **Writer ordering rules** (per connection, a single goroutine):
  1. `hello` is always first.
  2. On `notify` with hub state `stale`, if the last state sent is not
     `stale`: send `source_stale` **ahead of any queued samples** (priority),
     then discard queued samples with `sequence <= staleAtSequence`. Those
     samples are at least one timeout old; their loss shows up in the next
     sample's `droppedSincePrevious`.
  3. Before writing a sample, if the last state sent is not `source_live`,
     send `source_live` (`first_frame` or `source_resumed`) first.
  4. Consumers can therefore rely on: no sample follows `source_stale`
     without an intervening `source_live`.
- **Benign race (documented):** a frame that arrives in the instant the
  monitor declares stale yields `source_stale` followed immediately by
  `source_live`/`source_resumed`. Consumers must treat any `source_stale` as
  a segment break regardless of duration.
- Required tests: concurrent publish/timeout/connect under `-race`; stale
  while a client has a full backlog; stale -> resume ordering.

Collector shutdown is signalled with WebSocket close code `1001` and reason
`collector_shutdown`, not with a status message. This is a deliberate
deviation from plan section 9.1 ("closing" status): a close frame is the
protocol-native signal and cannot be stuck behind a queued sample.

Stream-level transcript fixtures (hello ordering, drop arithmetic,
stale/resume ordering, reconnect, close codes) are produced in Phase 2 from
real hub runs, as `testdata/v1/transcripts/*.jsonl`.

### Rules

1. **No fabricated steering.** `inputs.steering` is `null` and
   `capabilities.steering.available` is `false`. Never `0`. No canonical
   field (`wheel_rotation`, `steering_angular_velocity`,
   `wheel_steering_angle_front_*`, yaw, angular velocity) may populate it. A
   future verified source is an additive capability with unit, range, neutral
   point and provenance, plus new fixtures.
2. **Invalid values are nulled, not clamped.** If `Brake` or `Throttle` is
   NaN, +/-Inf or outside `[0, 100]`, that channel is sent as `null` and a
   server-side `invalid_inputs` counter increments. Consumers must treat
   `null` as "no reading", never as `0`. (The current decoder cannot produce
   such values; this is a defensive guard.)
3. **Loss accounting.** `Pipeline` increments `Sequence` only after a
   successful decode, immediately before `Publish`, so published sequences
   are contiguous (UDP gaps, dedup rejections and PackageID resets do not
   advance it). Every frame published **while a client is registered** is
   offered to that client. The writer derives
   `droppedSincePrevious = sequence - previousSentSequence - 1`, where
   `previousSentSequence` updates only after a successful write. It is 0 for
   the first sample on a connection, and 0 (no underflow) if the sequence
   does not advance, which only a non-pipeline library publisher can cause.
   Frames published before the client registered are not counted, and losses
   after the last delivered sample are never reported (the connection
   closes). Status messages never affect this count. `sourceGap` reports
   loss upstream of the collector; `droppedSincePrevious` reports loss inside
   the collector. They are independent.
4. **No backfill.** A new connection receives `hello` then only new frames.
   Reconnect yields a new `hello`; consumers must not stitch segments.
5. **`sessionId`** is 128 random bits (hex, 32 chars) from `crypto/rand`,
   fixed for the life of the server process. It is not a credential.
6. **Compatibility.** v1 consumers ignore unknown fields, unknown `status`
   states and messages with an unknown `type`. Removing or changing a
   field's meaning requires `gt7.inputs.v2`. Consumers reject any message
   whose `protocol` is not `gt7.inputs.v1`. `schema.json` is the collector's
   conformance test for what it emits today (it pins
   `steering.available` to `false`); consumers should apply the rules above
   rather than hard-failing on full schema validation, so that a future
   additive capability (e.g. verified steering) is not a breaking change.
7. **Encoding.** Compact JSON, one message per text frame, no compression
   (`permessage-deflate` disabled). Float32 values are encoded with
   float32 shortest-round-trip formatting (e.g. `74.5098`); integral values
   may appear without a fraction (`0`, `100`).
8. **Timestamp format.** `recvTimestamp` is Go `time.RFC3339Nano` of the UTC
   time: trailing fractional zeros are trimmed (0-9 digits), suffix `Z`.
   Consumers must parse it as RFC 3339 and must not rely on digit count.
9. **Schema limits.** The JSON Schema checks shape only. It cannot bound
   `sequence` to `<= 18446744073709551615` or reject impossible calendar
   dates. Go encoder tests enforce those, and consumers must parse
   `sequence` as an arbitrary-precision integer (`BigInt`).
10. **`hello.status`** is a closed enum in v1; `status.state` is open
    (consumers ignore unknown states).

Fixture note: `hello.no-steering.json` is a `hello` sent while the source is
stale. Its name is kept for the trainer's "steering unavailable" test case;
every v1 `hello` advertises steering as unavailable.

### Close codes

| Code | Reason | Meaning |
|---|---|---|
| 1001 | `collector_shutdown` | Collector stopping |
| 1008 | `read_only_stream` | Client sent a data message |
| 1009 | (library) | Client frame exceeded read limit |
| 4000 | `slow_consumer` | Client fell behind a write deadline (best effort; the socket may already be dead) |

Pre-upgrade rejections are plain HTTP responses (ADR 0003).

## Consequences

- The trainer is isolated from GT7 decode changes and protobuf churn.
- Roughly 250-300 bytes per sample; ~60 Hz -> ~18 KB/s per client.
- Steering-dependent drills must be disabled by the trainer in v1.

## Alternatives rejected

- Full canonical JSON dump: leaks unverified fields, large, couples consumers.
- Protobuf/binary frames: not justified until measured JSON cost is a
  problem (C-031).
- Sequence as JSON number: loses precision beyond 2^53.
- Per-client drop counter swapped atomically: misattributes drops across
  samples under races; the sequence difference is exact.
