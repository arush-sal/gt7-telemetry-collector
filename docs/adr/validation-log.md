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

Rows are UNVERIFIED unless a dated result is shown. Tested rows below were
run headless against a synthetic fixture sender (no PS5), see Phase 5.

| Browser / version | OS | Page origin | Collector URL | Permission prompt | Result | Date |
|---|---|---|---|---|---|---|
| Chrome 157.0.8103.38 (headless=new) | Ubuntu 24.04.5 | `http://localhost:5173` | `ws://127.0.0.1:8765/v1/stream` | none shown (headless; not a LNA test) | PASS: connects, `ws.protocol` = `gt7.inputs.v1`, hello then samples, token mode and stale/resume and 1001 shutdown pass; non-allowlisted origin `:5174` and wrong/missing token fail with 1006 (synthetic sender, no PS5) | 2026-11-14 |
| Chrome 158 (headless) | Linux | `http://localhost:4173` | `ws://127.0.0.1:8765/v1/stream` | none shown (headless); Permissions API reports LNA `prompt` for localhost -> 127.0.0.1 although no prompt appears | PASS (handshake only, reported by the trail-braking-trainer session; real collector v0.3.0 built from tag, `--rtps=none`, no PS5, so no samples): subprotocol `gt7.inputs.v1` negotiated, hello (`waiting_for_source`, steering reason) received, zero client sends; good token accepted, wrong/missing token rejected (collector logged token rejection); disallowed origin rejected (3 origin rejections logged) and the trainer UI showed the exact `--ws-origin` | 2026-11-14 |
| Chrome | - | `https://<site>.pages.dev` | `ws://127.0.0.1:8765/v1/stream` | LNA | UNVERIFIED | - |
| Chrome | - | `https://<site>.pages.dev` | `ws://192.168.x.y:8765/v1/stream` | LNA | UNVERIFIED | - |
| Chrome | - | `https://<site>.pages.dev` | `wss://collector.example.net/v1/stream` | - | UNVERIFIED | - |
| Firefox 157.0.1 (snap, headless) | Ubuntu 24.04.5 | `http://localhost:5173` | `ws://127.0.0.1:8765/v1/stream` | none shown (headless) | PASS: connects, hello then samples, stale/resume and 1001 shutdown pass; latency p95 18-167 ms (browser-side, see Phase 5); token mode and wrong-origin not run in Firefox (synthetic sender, no PS5) | 2026-11-14 |
| Firefox | - | `https://<site>.pages.dev` rows above | as above | LNA / mixed content | UNVERIFIED | - |
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

## Phase 2 performance result (2026-11-14)

Same host as baseline; `benchstat` over 10 interleaved runs.

| Benchmark | Median | allocs/op |
|---|---|---|
| `ingest.BenchmarkPipelineHandle` (no-op publisher) | 1.159 us +/-27% | 6 |
| `browserstream.BenchmarkPipelineWithHub` (4 draining clients) | 2.322 us +/-14% | 6 |

`Hub.Publish` alone: 0 clients 55 ns; 4 clients 300 ns (queues full) /
569 ns (draining); 64 clients 3.9 us / 14.6 us. 0 allocs/op in every case.

**Gate decision (orchestrator):** the hard gate (0 added allocs) passes.
The statistical gate (+10%) fails at +100%, but it compared a no-op
publisher against four real consumer goroutines. Nearly all of the added
cost is the runtime waking each parked writer goroutine on a channel send,
which any per-client-writer design pays and which the existing RTPS
publisher also pays for its own consumer. At 60 Hz the 4-client cost is
about 0.003% of one core, and about 0.09% at the 64-client cap. The gate is
revised to an absolute budget: `Hub.Publish` with 4 draining clients stays under
**2 us/op** on this host. The receive path never blocks or allocates for
WS. Revisit only if a profile from a live session shows otherwise.

Superseded gate: "statistically significant regression above 10% blocks
Phase 2" (Phase 0). Replaced for the reason above; raw results kept in the
session scratchpad (`bench-phase2-{base,hub}.txt`). The 60 Hz percentages
cover `Publish` only, not per-client projection, JSON or network writes,
which run on writer goroutines.

## Phase 2 peer review (Codex, 2026-11-14)

No blocking issues. Fixed: stalled-`Publish` false resume (per-client
monotonic discard cutoff, barrier test), ticker panic at 1ns timeout,
de-flaked slow-client and concurrent-ordering tests (stress
`-race -count=20 -cpu 1,2,8` green), best-effort drop-oldest documented,
hand-computed drop arithmetic, waiting-burst test, benchmark goroutines
joined, ADR 0002 ordering section updated for the accepted deviations.

## Phase 3 peer review (Codex, security focus, 2026-11-14)

Blocking, fixed: pre-auth listener pinning via withheld request bodies
(ReadTimeout/WriteTimeout 10s, body rejection with immediate read
deadline, real-listener regression test). Non-blocking, fixed: shutdown
deadline now force-closes and returns an error; close path bounded by a
raw `net.Conn` deadline (flooding client released within WriteTimeout+1s);
exact 1009 for oversized frames; Origin==Host shortcut, comma-combined and
duplicate Origin rejected; 431 for >8 KiB headers; ReadHeaderTimeout 3s
with residual risk documented. Fuzz: `FuzzNormalizeOrigin` 242k execs,
`FuzzParseSubprotocols` 198k execs, no crashers. govulncheck clean.

## Phase 4 peer review (Codex, 2026-11-14)

No lifecycle defects. Fixed: runtime errors (e.g. TLS file paths) now
redacted of all tokens in `mainExit`; pflag `%q`-quoted tokens redacted;
tests for raw-sink failure after `Listen` (port released), injected
`Serve` failure stopping the receiver, cleanup/timeout hygiene. `--rtps`
help text change recorded in ADR 0001. Remaining: three port TOCTOU spots
in tests (ports must be 1..65535 on the CLI path), documented in code.
Pre-existing: an RTPS publisher created before a later startup failure is
not explicitly stopped (no goroutine started; unchanged behaviour).

## Phase 5 validation (2026-11-14)

**No PS5 or GT7 was available. Everything below used a synthetic UDP sender
replaying the 16 `protocol/testdata/packet_*.bin` fixtures round-robin at
60 Hz with strictly increasing PackageIDs (ciphertext bytes 0x70..0x73
patched with old^new, as in `ingest/collector_bench_test.go`). Nothing here is
a live-hardware result.**

Environment: Ubuntu 24.04.5 (Linux 7.0.0-38-generic), AMD Ryzen 7 PRO 5850U
(16 threads), Go 1.27.4, Chrome 157.0.8103.38 (`--headless=new`), Firefox
157.0.1 (snap, `--headless`), HEAD `a34fa3b`. Collector flags:
`--ps5=127.0.0.1 --rtps=none --ws-listen=127.0.0.1:8765
--ws-origin=http://localhost:5173 --recv-port=34740 --send-port=34739`
(defaults for the rest). The receiver does not filter by source IP. Harness
(sender, static server with `POST /result`, Go WS client, test page, raw
results and logs) is kept outside the repo in the session scratchpad
(`.../scratchpad/phase5/`). The page runs 10 s and posts a JSON summary.

### Browser smoke (page `http://localhost:5173`, 10 s, 60 Hz)

| Test | Browser | Result |
|---|---|---|
| Connect, subprotocol, order | Chrome | PASS: 600 msgs (1 hello + 599 samples), first 3 types `hello,sample,sample`, `ws.protocol` = `gt7.inputs.v1`, 0 sequence discontinuities, sum(`droppedSincePrevious`) 0 |
| same | Firefox | PASS: 669 msgs (1 hello + 668 samples), same ordering, protocol, 0 discontinuities, drop sum 0 |
| Inputs | both | PASS: `inputs.steering` always null; brake 0..6.666667 (fixture range), throttle 0..100 |
| Latency `Date.now() - recvTimestamp` (ms resolution) | Chrome | p50 0, p95 1, p99 1, max 3 (n=599; repeated runs in 3 other Chrome tests: max 2-4) |
| same | Firefox | p50 2, p95 62, p99 126, max 171 (n=668). Other Firefox runs: p95 18 and 167, p99 113 and 249. Snap headless Firefox event-loop delay is the likely cause (the collector side showed 0 drops and 0 slow closes), but this was not isolated. Treat as an upper bound that includes browser scheduling. |
| Origin `http://localhost:5174` (not allowlisted) | Chrome | PASS (rejected): `error` then close 1006, 0 messages; `ws_rejected_origin` = 1 |
| Token mode (`GT7_WS_TOKEN` = 64 hex), offer `["gt7.inputs.v1","gt7.token.<tok>"]` | Chrome | PASS: connects, `ws.protocol === "gt7.inputs.v1"` (token not echoed), 239 msgs |
| Wrong token | Chrome | PASS (rejected): 1006, 0 messages, `ws_rejected_token` = 1 |
| No token offered while token set | Chrome | PASS (rejected): 1006, 0 messages, `ws_rejected_token` = 1 |
| Token in collector logs | all 3 token runs | PASS: 0 occurrences of the token in collector logs |
| Stale/resume (sender paused 3 s at t=4 s, `--ws-source-timeout` 2 s) | Chrome | PASS: `source_stale/no_accepted_frames_within_timeout` then `source_live/source_resumed` before the next sample; no sample followed stale without live; 539 samples, 0 discontinuities, drop sum 0 |
| same | Firefox | PASS: same two statuses in order (1.0 s apart), 0 discontinuities |
| SIGINT collector, page left open | Chrome | PASS: close 1001, reason `collector_shutdown`, wasClean true |
| same | Firefox | PASS: close 1001, reason `collector_shutdown`, wasClean true |

The `sourceGap`-free, pause-free stream shows continuous sequences because the
sender never skips PackageIDs. Page-initiated closes appear as 1005 (the test
page's own `ws.close()`).

### Soak (30 min at 60 Hz, 2026-11-14 03:31-04:01 local, plus ~1 min wind-down)

Three Go clients (`github.com/coder/websocket`, Origin `http://localhost:5173`):

| Client | Result |
|---|---|
| Fast (reads everything) | PASS: 110028 samples received, 0 sequence gaps, sum(`droppedSincePrevious`) 0. The sender emitted 108000 by 04:01:12; the extra ~2000 are the ~35 s before I stopped the harness. |
| Connect/disconnect churn (dial, read hello, close, vary 0-600 ms) | PASS: 6068 iterations, 0 failures, 0 dial errors; `ws_clients` returned to 0 at shutdown |
| Slow, reads 1 msg/s | Closed by the server within one stats window (about 10-20 s) with **0 queue drops**: the liveness ping is stuck behind the kernel-buffered backlog, so the peer is removed as dead. It stayed connected on its side draining stale data. |
| Slow, reads 50 msg/s (below 60 Hz), reconnects on close | 36 reconnects over ~25 min, 75880 samples, sum(`droppedSincePrevious`) 0. Same dead-peer mechanism. |

**Gap in evidence:** the in-hub queue-drop and `slow_consumer` (4000) paths
were NOT exercised by the soak. Over loopback the kernel send buffer absorbs
well over 15 s of 60 Hz traffic (observed Send-Q 234 KB), so the ping timeout
(10 s interval + 5 s timeout) removes a stalled reader before a write
blocks. Final counters: `ws_dropped` 0, `ws_slow_closed` 0. Drop arithmetic
stays covered by the Phase 2 unit tests only. A real stalled browser tab
behaves like the 1 reads/s client.

Collector process (samples every 60 s, `/proc/<pid>`):

| Metric | First (03:32) | Last (04:01) | Max |
|---|---|---|---|
| RSS | 33544 kB | 34876 kB | 35200 kB |
| Threads | 18 | 21 | 21 (stable from 03:36) |
| FDs | 8 | 8 | 9 |
| CPU (interval) | 2% | 4% | 4% |

CPU: lifetime `ps %cpu` 2.4 at the first sample and 2.8 at the last, with
~3 clients plus churn on a 16-thread host; interval CPU 2-4% of one core.
The split between receive path, JSON projection and churn was not profiled.
RSS deciles (10-min means): 34.01 MB, 34.36 MB, 34.70 MB. That is a drift
of about +0.35 MB per 10 min (1%), with threads and FDs flat. At this
duration it cannot be separated from allocator/GC variance and cannot rule
out a slow leak. **Verdict: no growth that matters over 30 min; a 4 h soak
would settle it.**

Final counters (`collector stopped`): published 110762, valid 110762,
foreign/short/stale/resets/gaps/raw_dropped all 0, `ws_accepted` 6108,
`ws_sent` 194332, `ws_dropped` 0, `ws_slow_closed` 0, `ws_invalid_inputs` 0,
`ws_stale_discarded` 0, `ws_rejected_*` all 0 (soak clients were all
valid), state `source_live`. 184 periodic `telemetry stats` lines were
logged. **0 warn/error log lines** in the collector log.

### Still UNVERIFIED

- Live PS5 / GT7 hardware (C-028): cadence, paused/menu behaviour, brake and
  throttle sweeps, real packet loss. `--ws-source-timeout` 2 s stays
  provisional.
- Chrome LNA prompt and HTTPS-origin pages (`https://<site>.pages.dev` ->
  `ws://127.0.0.1`), mixed content (C-029).
- LAN binds, `wss://`, TLS and non-loopback token flow.
- Safari, and Firefox/Chrome on other OSes or versions.
- Driver steering (C-030): still null by design.
- Hub queue-drop and `slow_consumer` close under a real network path.
- Long-duration (hours) memory behaviour.

### Phase 5 notes (orchestrator)

- A client that reads slowly is disconnected by ping/pong liveness
  (its pong queues behind the kernel-buffered backlog) before its hub queue
  overflows. Resources stay bounded either way. The hub drop-oldest and
  `slow_consumer` paths are covered by unit/integration tests, not by
  this soak.
- Firefox headless (snap) latency tail is unexplained; collector showed
  0 drops. Re-measure with a non-snap build before drawing conclusions.

## Release v0.3.0 (2026-11-14)

- Tag `v0.3.0` (signed) on `master` `378eb89`; release workflow green
  (Verify tag, Security incl. OSV-Scanner, Tests, GoReleaser).
- 26 assets. Local verification of linux/amd64: `sha256sum -c` OK,
  `cosign verify-blob` with the release.yml tag identity "Verified OK",
  `gh attestation verify` exit 0, binary reports `0.3.0` and lists the
  `--ws-*` flags.
- Repository settings changed: `google/osv-scanner-action/osv-scanner-action@*`
  added to the Actions allowlist; rulesets `protect-master` (no deletion,
  no force-push) and `immutable-release-tags` (`refs/tags/v*`: no
  deletion, update or force-push).
