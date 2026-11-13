# ADR 0001: Optional WebSocket server as a logical browser sink

- Status: Proposed (Phase 0)
- Date: 2026-11-14
- Starting commit: `ee8633af575d60630a7fa71c04236edb9971cc59` (`master`)
- Requirements: C-001..C-006, C-012, C-017, C-025..C-027

## Context

The collector receives GT7 UDP packets on one goroutine and runs each through
`ingest.Pipeline.Handle` (tee raw -> decrypt -> dedup -> decode ->
`telemetry.ToCanonical` -> `Publisher.Publish`). The only output today is
`transport.Publisher`, an outbound RTPS client (gRPC, Unix, HTTP(S)) with a
single-producer, drop-oldest bounded queue. The CLI always constructs it
(`--rtps` defaults to `localhost:50051`).

A separate browser application (Trail Braking Trainer) needs live
brake/throttle input. A browser cannot accept inbound connections, so the
collector must listen and the browser must dial it.

## Decision

1. Add a new package `browserstream` that owns an `http.Server` exposing
   `GET /v1/stream` (WebSocket, subprotocol `gt7.inputs.v1`) and
   `GET /healthz`. The collector is the WebSocket **server**; the browser is
   the WebSocket client and the logical telemetry sink.
2. `browserstream.Hub` implements `ingest.Publisher`. The CLI combines it with
   the existing RTPS publisher through an unexported fan-out type in
   `cmd/gt7-collector` (a slice of `ingest.Publisher` whose `Publish` calls each
   destination in turn). Every destination is itself non-blocking, so the
   fan-out needs no queue of its own. When only one destination is enabled
   it is passed to `ingest.NewPipeline` directly.
3. One receiver, one pipeline, one decode per packet. No second UDP socket,
   no second decode path, no change to `ingest.Publisher`'s signature, no
   change to `transport.New` or `transport.Publisher`.
4. `Hub.Publish` is **single-producer** (the receive goroutine, same contract
   as `transport.Publisher.Publish`). It does only: one
   `atomic.Pointer[[]*client]` load, atomic stores of the last-frame
   monotonic time and last sequence, and for each client a non-blocking
   drop-oldest enqueue of the frame pointer into that client's bounded
   channel. No JSON, no network I/O, no mutex, no allocation (0 allocs/op is a
   test gate). Register/unregister rebuild the slice under a mutex that
   `Publish` never takes and swap the pointer (copy-on-write).
   **Teardown rule:** a client's sample channel is **never closed**. Because
   `Publish` may still hold a stale snapshot that includes a just-removed
   client, unregister only swaps the snapshot and closes a separate `done`
   channel (via `sync.Once`); the writer selects on `done`. The orphaned
   channel is bounded and garbage-collected. The drop-oldest receive in
   `Publish` and the writer are the only two readers of a sample channel.
5. Each client has its own writer goroutine that projects and marshals the
   frame (ADR 0002) and writes it with a bounded write deadline. A slow client
   only overflows its own queue; RTPS and other clients are unaffected.
6. Canonical frames are treated as immutable after `Publish`. RTPS transports
   only marshal; the browser projection only reads. Verified with `-race`
   and with a test that `proto.Clone`s a frame before publishing and asserts
   `proto.Equal` after every consumer has processed it (catches
   non-concurrent mutation that `-race` cannot).
7. Operating modes (C-005):
   - RTPS only: today's default; no listener is bound.
   - WS only: `--rtps=none` plus `--ws-listen`; no RTPS publisher is built,
     so no reconnect warnings.
   - Both: RTPS and WS receive the same frame pointer through the fan-out.
   - `--rtps=none` without `--ws-listen` is a configuration error (exit 2).
   - `--rtps=none` with a non-empty `--rtps-token` is a configuration error
     (exit 2). A set `INGEST_TOKEN` is ignored when RTPS is off (it may be set
     globally); the plaintext-token warning is skipped.
   - `none` is not a valid RTPS address today, so the value is additive.
8. Lifecycle (C-017): the receiver remains the only goroutine that sees the
   signal context. When it returns, publisher, WS server and reporter contexts
   are cancelled. Shutdown order: stop receiver -> close WS listener and send
   close frames (1001) to clients with a bounded deadline -> stop RTPS
   publisher -> close raw sink -> log final counters. A bind failure for the
   WS listener happens before any worker starts (listen first, then serve).

## Proposed CLI (final names; enforced by tests in Phase 4)

| Flag / env | Default | Meaning |
|---|---|---|
| `--rtps=none` | not default | Disable RTPS publishing; requires `--ws-listen` |
| `--ws-listen` | empty (off) | `host:port` for the HTTP/WS listener |
| `--ws-origin` | none | Repeatable exact browser origin; required when `--ws-listen` set |
| `--ws-max-clients` | `4` | Concurrent WebSocket clients (1..64) |
| `--ws-client-buffer` | `64` | Per-client sample queue (1..4096) |
| `--ws-write-timeout` | `2s` | Per-message write deadline (100ms..60s) |
| `--ws-source-timeout` | `2s` | No-frame interval before `source_stale` (100ms..60s); calibrate live |
| `--ws-allow-insecure-lan` | `false` | Permit plaintext `ws://` on a non-loopback bind |
| `--ws-tls-cert`, `--ws-tls-key` | empty | Serve `wss://` directly; both or neither |
| `GT7_WS_TOKEN` (env only) | unset | Access token; required for non-loopback binds |

There is deliberately **no** `--ws-token` flag (command-line arguments are
visible in `ps`). Security rules for these flags are in ADR 0003.

## Public API impact

Additive only:

- New package `browserstream` (new exported types).
- No change to `ingest`, `transport`, `telemetry`, `protocol`, `replay`,
  `rawsink` or the canonical protobuf.

**Needs approval (not required for the CLI path):** `ingest.Pipeline.Handle`
hard-codes `Source_SOURCE_LIVE`. The CLI only ever feeds live packets, so CLI
output is correct, but a library user who drives `replay.Source.Play` into
`Pipeline.Handle` gets frames labelled LIVE. Proposed additive fix: a
variadic constructor option, `ingest.NewPipeline(raw, pub, ingest.WithSource(telemetryv1.Source_SOURCE_REPLAY))`
(default LIVE; fixed at construction so there is no setter race). Adding a
variadic parameter keeps every existing call site compiling; it would only
break code that stores `ingest.NewPipeline` in a typed function variable.
A wrapper that rewrites `Source` after the fact is ruled out because frames
are immutable after `Publish`. Without approval it is recorded as a known
limitation, C-010 stays open for library replay, and the WS projection
forwards whatever `Source` the canonical frame carries.

## Consequences

- Without `--ws-listen` the binary binds nothing new and behaves as today.
- Adds one Go module dependency (`github.com/coder/websocket`, pinned; see
  ADR 0003), tracked by Dependabot and listed in the Syft SBOM.
- Per-client marshaling costs O(clients) JSON encodes per frame (max 64,
  default 4) on writer goroutines, never on the receive goroutine.

## Alternatives rejected

- Outbound WebSocket via `transport.New`: wrong direction; browsers cannot
  listen.
- Central hub goroutine with a dispatch channel: adds a shared queue whose
  overflow would drop frames for every client at once; copy-on-write snapshot
  is simpler and keeps drops per client.
- Exported `ingest.Fanout`: no consumer outside the CLI needs it yet.
