# Browser streaming: validation log

Evidence log for the browser-streaming extension (ADRs 0001-0003). Every
live-device or browser claim stays **UNVERIFIED** until a dated entry here
records the test.

## Phase 0 baseline (2026-11-14)

| Item | Value |
|---|---|
| Starting commit | `ee8633af575d60630a7fa71c04236edb9971cc59` (`master`) |
| Work branch | `feat/browser-streaming` (worktree `.worktrees/browser-streaming`) |
| Go | go1.27.4 linux/amd64 (go.mod `go 1.27.4`; latest stable per go.dev) |
| `go mod verify` / tidy | all modules verified / no diff |
| `go build ./...`, `go vet ./...` | pass |
| `golangci-lint` v2.15.0 | 0 issues |
| `govulncheck` | no vulnerabilities |
| `trivy fs .` (local v0.71.0) | 0 vulnerabilities (go.mod, docs/go.mod) |
| `zizmor .github/workflows` | 2 low (`self-repository` hints at `release.yml:18,26`), 0 medium/high; CI gate is `min-severity: high` |
| `go test -race -covermode=atomic ./...` | all pass on the main checkout; total coverage 55.7% (generated protobuf at 0%) |

Per-package coverage: cmd/gt7-collector 76.8%, gt7data 100%,
gt7data/internal/gen 74.0%, ingest 86.2%, protocol 93.8%, rawsink 76.8%,
replay 87.5%, telemetry 90.0%, transport 85.6%.

**Environmental failure (pre-existing, not a regression):**
`TestUnixPublisher` fails inside the worktree with
`listen unix .../transport/u940663332/s: bind: invalid argument`. The test
creates its socket under `.` (`os.MkdirTemp(".", "u")`), and the worktree
path pushes it past the 108-byte `sun_path` limit. The same test passes
on the main checkout at the same commit. Proposed fix (needs approval, it touches
an unrelated test): use `os.MkdirTemp("", "u")`.

Benchmarks: the repository had none. Phase 0 adds
`ingest/collector_bench_test.go` (test-only) to establish the receive-path
baseline; results are below.

### Receive-path baseline

Host: AMD Ryzen 7 PRO 5850U, 16 threads, Go 1.27.4 linux/amd64.
Command: `go test -run='^$' -bench=. -benchmem -count=6 ./ingest/ ./transport/`.

| Benchmark | ns/op (6 runs) | B/op | allocs/op |
|---|---|---|---|
| `BenchmarkPipelineHandle` (accepted path, 16 real fixtures) | 1467, 1519, 1562, 1672, 2053, 2689 (median ~1617) | 1078 | 6 |
| `BenchmarkPublisherPublishDropOldest` (RTPS queue full) | 37.5, 37.9, 40.0, 41.3, 42.8, 55.6 | 0 | 0 |

The laptop host is noisy (no CPU pinning), so treat ns/op as order-of-magnitude.
Allocations are deterministic. At 60 Hz the receive path spends about 0.01% of
one core. Run-to-run noise is about 40%, so a fixed ns budget is not
defensible from these numbers. **Phase 2 gate (proposed):**

- Hard gate: attaching the hub adds **0 allocs/op** to `Pipeline.Handle`.
- Statistical gate: `benchstat` over `-count=10`, baseline and hub runs
  interleaved on the same host. The median delta is reported, and a
  statistically significant regression above 10% blocks Phase 2 pending
  review.
- Scenarios: 0, 4 and 64 clients; draining vs stalled client;
  connect/disconnect churn; RTPS + WS together.

## Existing behavior inventory (must not change with WS disabled)

| Behavior | Location | Guard |
|---|---|---|
| `--ps5` required IP; `--rtps` default `localhost:50051`; ports, heartbeat, buffers, log level validation; exit 2 on config error | `cmd/gt7-collector/main.go` `parseFlags`, `mainExit` | `TestParseFlags`, `TestMainExitCodes` |
| `INGEST_TOKEN` / `--rtps-token` precedence and redaction | `parseFlags` | `TestTokenPrecedence`, `TestTokenNotInParseError`, `TestTokenRedactionBeforeParsing` |
| Plaintext-token warning | `warnPlaintextToken` | `TestPlaintextTokenWarning` |
| Raw PCAP capture | `run`, `rawsink` | `TestRunCreatesPCAP` |
| `--version` output | `mainExit` | `TestVersionFlag` |
| Decrypt -> dedup -> decode -> map -> publish, counters | `ingest/collector.go` | `ingest` tests |
| Drop-oldest RTPS queue, reconnect/backoff, edge-triggered warn | `transport/publisher.go` | `transport` tests |
| gRPC / Unix / HTTP(S) transports, bearer auth | `transport/transports.go`, `auth.go` | `transport` tests |
| PCAP/PCAPNG replay pacing | `replay/` | `replay` tests |
| Shutdown order: receiver -> publisher -> raw sink -> final counters | `run` | `TestRunCreatesPCAP` (partial) |

## Input channel / source validation table

| Channel | Canonical field | Decode | Evidence | Status for `gt7.inputs.v1` |
|---|---|---|---|---|
| Brake | `brake` (float, 0-100) | byte / 2.55 (`protocol/decode.go:49`) | golden fixtures (only one non-zero sample: 6.666667 in `packet_03`) | **Exposed**; range confirmed by decode math; live released/partial/full sweep UNVERIFIED (C-028) |
| Throttle | `throttle` (float, 0-100) | byte / 2.55 (`protocol/decode.go:48`) | golden fixtures span 0..100 | **Exposed**; live sweep UNVERIFIED (C-028) |
| Driver steering input | none | - | no field identified | **Unavailable** (`driver_steering_input_unverified`) |
| `wheel_rotation` | `wheel_rotation` | Format-C tail | proto: "unconfirmed" | Not exposed; may not be used as steering |
| `steering_angular_velocity` | same | Format-C tail | proto: "unconfirmed" | Not exposed |
| `wheel_steering_angle_front_left/right` | same | Format-C tail | proto: "CONFIRMED", vehicle front-wheel angle | Not exposed: vehicle output, not driver command |
| `throttle_filtered` | uint 0-255 | Format-C tail | proto: "CONFIRMED, tracks throttle trace" | Not exposed in v1 (redundant with `throttle`) |
| `brake_filtered` | uint 0-255 | Format-C tail | proto: "unconfirmed, constant 0 in captures" | Not exposed |
| Source | `source` | `Pipeline.Handle` hard-codes LIVE | code | CLI: always LIVE (correct). Library replay mislabels as LIVE (ADR 0001, needs approval) |
| Gap | `gap` | dedup | `ingest` tests | Exposed as `sourceGap` |
| Sequence | `sequence` | pipeline counter, contiguous per published frame | code | Exposed as decimal string; basis for `droppedSincePrevious` |
| Receive time | `recv_timestamp` | receiver wall clock | code | Exposed as `recvTimestamp` |

## Requirement traceability (planned)

| ID | Phase | Planned evidence |
|---|---|---|
| C-001, C-006, C-027 | 0, 4 | existing test suite unchanged + legacy-flags test; no listener bound without `--ws-listen` |
| C-002, C-003 | 3, 4 | `browserstream/server_test.go` httptest + real WS client receives `hello` then samples |
| C-004 | 2, 4 | fan-out test: one `Handle` -> same frame pointer to both sinks; one decode |
| C-005 | 4 | flag matrix: RTPS-only, WS-only, both, neither (exit 2) |
| C-007, C-021 | 1 | `browserstream/testdata/v1` schema + fixtures; encoder golden tests |
| C-008 | 1, 2 | projection test vs `ToCanonical` output from real fixtures |
| C-009 | 1 | encoder never emits steering != null; schema `invalid/` cases |
| C-010 | 1 | projection tests for timestamp, sequence, source, gap, sessionId |
| C-011 | 2 | slow-client test: `droppedSincePrevious` sum equals that client's queue drops, excluding drops after the last delivered sample |
| C-012, C-026 | 2, 3 | slow + fast client test, RTPS fake keeps rate; connect/disconnect loop with goroutine check |
| C-013..C-016 | 3, 4 | negative security tests in ADR 0003 table |
| C-017 | 3, 4 | cancellation/bind-failure tests; goroutine leak check |
| C-018 | 2 | status state machine tests (waiting, live, stale, resumed, shutdown close) |
| C-019 | 4 | counters in periodic/final log lines (observer test) |
| C-020 | 1-5 | unit, integration, negative, race, soak (soak manual/nightly) |
| C-022, C-023 | 6 | CI and release workflows extended; tag-on-master guard; dry run |
| C-024 | 6 | README + Hugo docs pages |
| C-025 | all | review checklist: no trainer logic, storage or accounts |
| C-028..C-030 | 5 | live PS5 + browser matrix entries in this log |
| C-031, C-032 | - | P1, deferred until measurements justify them |

## Browser compatibility matrix

All rows UNVERIFIED.

| Browser / version | OS | Page origin | Collector URL | Permission prompt | Result | Date |
|---|---|---|---|---|---|---|
| Chrome | - | `http://localhost:5173` | `ws://127.0.0.1:8765/v1/stream` | - | UNVERIFIED | - |
| Chrome | - | `https://<site>.pages.dev` | `ws://127.0.0.1:8765/v1/stream` | LNA | UNVERIFIED | - |
| Chrome | - | `https://<site>.pages.dev` | `ws://192.168.x.y:8765/v1/stream` | LNA | UNVERIFIED | - |
| Chrome | - | `https://<site>.pages.dev` | `wss://collector.example.net/v1/stream` | - | UNVERIFIED | - |
| Firefox | - | as above | as above | - | UNVERIFIED | - |
| Safari | - | as above | as above | - | UNVERIFIED | - |

## Open risks

1. Browser LNA / mixed-content behavior (C-029): unverified.
2. Source cadence and paused/menu behavior (C-028): unverified, so the
   `--ws-source-timeout` default of 2s is provisional.
3. True driver steering (C-030): no candidate field; separate evidence track.
4. Library replay source labelling: needs an approved additive API.
5. `TestUnixPublisher` path length inside worktrees: needs an approved
   test-only fix.
6. Fixture coverage for brake is thin (one non-zero sample). A live capture
   with a full brake sweep is needed before claiming range behavior.

## Phase 0 peer review (Codex, 2026-11-14)

Blocking findings, all resolved in the ADRs before Phase 1:

1. Source-state ownership/ordering: replaced per-writer timers with one
   hub-owned state + monitor goroutine, register-then-snapshot `hello`,
   priority `source_stale` with backlog discard by `staleAtSequence`
   (ADR 0002).
2. Channel teardown: sample channels are never closed; `done` channel +
   `sync.Once`; single-producer `Publish` stated (ADR 0001).
3. Pre-auth/dead-peer exhaustion: `LimitListener`, post-auth slot
   reservation, ping/pong liveness, bounded close (ADR 0003).
4. Origin canonicalization vs `coder/websocket` glob matching: escaped
   patterns in both port forms; corrected the negative-test row (ADR 0003).

Non-blocking findings adopted: loss-accounting caveats, `WithSource`
constructor option instead of a setter, immutability clone test,
proxy/Host guidance, IPv6/trailing-dot Host parsing, token rotation and
proxy log redaction, browser failure visibility (deviation recorded),
unknown-`type` handling, schema-as-conformance-test rule, statistical
performance gate, C-031/C-032 deferred, `--rtps=none` token semantics,
transcript fixtures scheduled for Phase 2.
