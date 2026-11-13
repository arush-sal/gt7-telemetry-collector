# ADR 0003: Browser WebSocket threat model, origin, auth and LAN policy

- Status: Proposed (Phase 0)
- Date: 2026-11-14
- Requirements: C-013..C-016, C-019, C-022, C-026, C-029

## Assets

Live driver input trace and its timing (behavioral data), the PS5/collector
LAN addresses, collector availability (and therefore RTPS delivery), the
`GT7_WS_TOKEN` and the existing RTPS ingest token, and LAN integrity.

## Attackers

1. Malicious website open in the user's browser (cross-site WebSocket
   hijacking, DNS rebinding).
2. Unauthorized LAN host.
3. Passive LAN observer when plaintext `ws://` is used.
4. Buggy or slow client (stalls, never reads).
5. Handshake/message spammer (huge headers, many connections, large frames).
6. Compromised dependency or untrusted PR in CI.

## Trust boundaries

- UDP from the PS5 (unchanged; existing Salsa20/magic validation).
- **New:** inbound HTTP/WebSocket on `--ws-listen`. Everything received here is
  untrusted: Host, Origin, `Sec-WebSocket-Protocol`, frames.
- Browser same-origin policy does **not** protect WebSockets; the server must
  check `Origin` itself.

## Decisions

### Bind policy

- `--ws-listen` host must be an IP literal or `localhost`. `localhost` is
  rewritten to `127.0.0.1` before binding (no DNS lookup). An empty host
  (`:8765`), `0.0.0.0`, `::` or any non-loopback IP is a **non-loopback**
  bind. Hostnames other than `localhost` are rejected (exit 2) to avoid
  ambiguous DNS-dependent exposure.
- Loopback = `127.0.0.0/8` or `::1` (an IPv4-mapped `::ffff:127.x.y.z` is
  treated as its IPv4 form). Use `[::1]:8765` for IPv6 loopback; the
  `localhost` rewrite binds IPv4 only. IPv6 zone IDs (`[fe80::1%eth0]`) are
  non-loopback binds.

### Origin allowlist (all modes)

- `--ws-origin` is required whenever `--ws-listen` is set (no implicit dev
  default).
- Each value must parse as `scheme://host[:port]` with scheme `http` or
  `https`, no path (a single trailing `/` is rejected, not trimmed), no query,
  fragment or userinfo, no `*`, not `null`. Scheme and host are lowercased;
  default ports (`:80` for http, `:443` for https) are stripped. Ports must be
  canonical decimal 1..65535 (no leading zeros) and `%` escapes are rejected,
  so the library's raw-host match can never disagree with ours. Comparison
  with the request `Origin` uses the same normalization and is **exact
  string equality**: no suffix, prefix, wildcard or regex matching.
- Requests to `/v1/stream` with a missing, `null` or unlisted `Origin` are
  rejected with HTTP 403 **before** upgrade. Non-browser clients must send an
  allowlisted `Origin` header.
- The pre-upgrade check above is **the** control. `coder/websocket`
  (v1.8.15) itself accepts a missing Origin and an Origin whose host equals
  the request Host, ignores the scheme, and matches `OriginPatterns` against
  the raw `host[:port]` with `path.Match` glob syntax. As defense in depth
  it is given `OriginPatterns` built from the allowlist with glob
  metacharacters escaped (IPv6 `[`/`]` included), in both the explicit
  default-port and no-port forms, so an origin our check accepts can never
  be refused inside `Accept`. `InsecureSkipVerify` is never set. Tests cover
  IPv6 origins, explicit default ports and mixed case.

### Host header (DNS rebinding)

- For a loopback bind, requests whose `Host` is not a loopback IP literal or
  `localhost` (with any port) are rejected with HTTP 421 on every path,
  including `/healthz`. This blocks DNS-rebinding reads of the local server.
  Host is parsed with `net.SplitHostPort` (bracketed IPv6 accepted);
  `localhost.` (trailing dot) and any other name are rejected.
- A TLS-terminating reverse proxy in front of a loopback bind must forward
  `Host: 127.0.0.1:<port>` (e.g. nginx `proxy_set_header Host 127.0.0.1:8765;`)
  and pass the browser's `Origin` and `Sec-WebSocket-Protocol` through
  unchanged. No allowed-host flag in v1.
- For non-loopback binds the token is the control; Host is not restricted.

### Authentication (token)

- Source: `GT7_WS_TOKEN` environment variable only. No flag, no file in v1.
- Required for any non-loopback bind (exit 2 if missing). Optional for
  loopback; when set it is enforced for loopback too.
- Syntax: 32..256 characters from `[A-Za-z0-9._~-]`. These are valid
  RFC 6455 subprotocol token characters. Generate with
  `openssl rand -hex 32`. Invalid syntax is exit 2 with the value redacted.
- Carrier: browsers cannot set `Authorization` on `new WebSocket()`. The
  client offers two subprotocols:
  `new WebSocket(url, ["gt7.inputs.v1", "gt7.token." + token])`.
- Server parsing of `Sec-WebSocket-Protocol` (all header lines, comma
  separated, whitespace trimmed): exactly one `gt7.inputs.v1` must be
  present; zero or one `gt7.token.*` entry; more than one token entry,
  empty entries, more than 8 entries total, or a case variant of
  `gt7.inputs.v1` (the library matches case-insensitively and would echo the
  variant) -> HTTP 400.
- Comparison: `subtle.ConstantTimeCompare` of SHA-256 digests of the offered
  and configured tokens (avoids leaking length).
- Missing or wrong token -> HTTP 401 with a fixed body; the offered value is
  never logged or echoed.
- The server negotiates **only** `gt7.inputs.v1`; the token entry is never
  selected and never appears in the response.
- Origin and token are independent checks; both are required for
  non-loopback binds. Origin is checked first, then token.
- Limitation recorded: the token is visible to anyone with devtools on the
  trainer page and to any TLS-terminating proxy. It authorizes read-only
  access to input telemetry only. Proxies must not log
  `Sec-WebSocket-Protocol`.
- Rotation/revocation: restart the collector with a new `GT7_WS_TOKEN`;
  existing connections close with 1001 and must re-authenticate.
- **Failure visibility (deliberate deviation from plan section 8).**
  Rejections happen before upgrade, so a browser sees only close code 1006
  with no reason; HTTP status codes are visible only to non-browser clients
  and devtools. Accept-then-close (e.g. 4401) was rejected: it would
  complete a WebSocket handshake for unauthenticated peers and widen the
  pre-auth surface. The trainer should show a fixed checklist (collector
  running, `/healthz` reachable, origin allowlisted, token set). The
  collector's DEBUG log records the rejection reason.

### Transport security for non-loopback binds

- `--ws-tls-cert` + `--ws-tls-key` -> `wss://` with Go's default TLS config
  (TLS 1.2 minimum). Both or neither (exit 2). Files are loaded before any
  port is bound; a load failure exits 1 with the file path but no key
  material.
- Without TLS, a non-loopback bind requires `--ws-allow-insecure-lan`
  (exit 2 otherwise) and logs a WARN at startup that input telemetry and the
  token cross the LAN in plaintext.
- Reverse-proxy TLS termination is documented as an alternative; the
  collector still enforces Origin and token.

### Resource limits

| Control | Value |
|---|---|
| `http.Server.ReadHeaderTimeout` | 3s |
| `http.Server.ReadTimeout` / `WriteTimeout` | 10s / 10s (plain requests and handshakes only; net/http clears both on Hijack, so established WebSockets are unaffected, tested) |
| Request bodies | Any request with `Content-Length != 0` or `Transfer-Encoding` -> HTTP 400 with `Connection: close` before routing; the read deadline is expired so net/http's post-handler body drain cannot pin a connection slot |
| `http.Server.MaxHeaderBytes` | 8 KiB |
| `http.Server.IdleTimeout` | 30s (does not apply to upgraded, hijacked connections) |
| Accepted TCP/TLS connections (incl. in-flight handshakes and upgraded clients) | `netutil.LimitListener(max-clients + 16)`; excess connections wait in the kernel backlog |
| Max concurrent clients | `--ws-max-clients` (default 4); a slot is reserved atomically **after** Origin/Host/token checks pass, so unauthenticated requests cannot hold slots; excess -> HTTP 503 before upgrade |
| Per-client queue | `--ws-client-buffer` (default 64), drop-oldest |
| Write deadline | `--ws-write-timeout` (default 2s); exceeded -> close 4000, counted |
| Liveness | server ping every 10s; no pong within 5s -> close, slot released (detects dead peers while the source is silent) |
| Read limit | 1 KiB per data message (control frames are not data); the stream is read-only and any data message closes 1008 `read_only_stream` (over-limit: library 1009); a reader loop always runs so pongs and close frames are processed. Not `conn.CloseRead`: in v1.8.15 it closes from inside its own goroutine and then waits up to 15s for itself |
| Close handshake | bounded by the write timeout via a socket deadline on the hijacked connection (the library's own close timeouts are 5s/15s) |
| Shutdown | handlers and `http.Server.Shutdown` bounded by write timeout + 1s; past that, remaining connections are force-closed and `Serve` returns an error |
| Compression | disabled |
| Methods / paths | `GET /v1/stream`, `GET /healthz`; others 404/405 |

### Logging and redaction

- Never log `Sec-WebSocket-Protocol`, `Authorization`, `Cookie`, request
  bodies, or sample payloads. No HTTP access log.
- Rejections are counted by reason (`origin`, `token`, `subprotocol`,
  `capacity`, `host`) and summarized in the periodic stats line; individual
  rejections log at DEBUG with remote address and reason only.
- `parseFlags` redaction is extended to `GT7_WS_TOKEN`.

### `/healthz`

Returns `200 {"status":"ok"}` with `Cache-Control: no-store`. No telemetry,
version, client counts, addresses or secrets. Subject to the Host rule.
No CORS headers are sent on any endpoint.

### Metrics (log fields, no endpoint in v1)

`ws_clients`, `ws_accepted`, `ws_rejected_{origin,token,subprotocol,capacity,host}`,
`ws_sent`, `ws_dropped` (sum of per-client queue drops), `ws_slow_closed`,
`ws_invalid_inputs`, `ws_source_state`. RTPS `pub_dropped` stays separate.

## Threat -> control -> negative test

| Threat | Control | Negative test (Phase 3/4) |
|---|---|---|
| Malicious site opens WS | Exact Origin allowlist pre-upgrade | wrong origin, `null`, missing, suffix `trainer.example.com.evil.test`, prefix, other port, other scheme -> 403; allowlisted origin in different case or with explicit default port -> accepted; IPv6 origin `http://[::1]:5173` -> accepted when listed |
| DNS rebinding | Loopback Host check | `Host: evil.test`, `localhost.` on loopback -> 421 for `/v1/stream` and `/healthz` |
| Pre-auth exhaustion / dead peers | LimitListener, post-auth slot reservation, ping/pong | many unauthenticated handshakes do not consume client slots; silent dead peer released within 15s |
| Unauthorized LAN client | Token pre-upgrade | none, wrong, two token entries, wrong-length -> 401/400 |
| Token in URL | Subprotocol carrier only; query string ignored and never logged | `?token=` does not authenticate |
| Token echoed | Only `gt7.inputs.v1` negotiated | response `Sec-WebSocket-Protocol` == `gt7.inputs.v1`; body/headers never contain token |
| Token in logs/errors | Redaction, no header logging | observer logs contain no token across failure paths |
| Client sends data | Read-only, 1 KiB limit | text frame -> close 1008; 1 MiB frame -> close 1009 |
| Connection exhaustion | Max clients, header timeout/limit, body rejection | N+1th upgrade -> 503; slowloris header -> closed after ReadHeaderTimeout; header > 8 KiB -> 431; max-clients+16 requests withholding a declared body do not block `/healthz`; goroutine count returns to baseline |
| Slow client | Per-client queue, write deadline | stalled reader: own drops grow, fast client and RTPS fake keep full rate |
| Plaintext LAN sniffing | TLS or explicit insecure opt-in | non-loopback without TLS or opt-in -> exit 2 |
| Accidental exposure | Loopback-first, non-loopback needs token + origin + TLS/opt-in | flag matrix tests |
| Dependency compromise | Pinned module, govulncheck, Trivy, OSV, SBOM, Dependabot | CI gates |

## Library choice

`github.com/coder/websocket` (latest at Phase 0: `v1.8.15`). Context-aware,
supports `AcceptOptions.Subprotocols`, `OriginPatterns`,
`CompressionMode`, `SetReadLimit` and `CloseRead`. Phase 3 pins the version
and adds tests proving its origin and subprotocol behavior rather than
assuming it.

## Residual risks (open)

- Browser Local Network Access prompts and mixed-content rules (C-029) are
  unverified; must be tested in Chrome, Firefox and Safari.
- Self-signed certificates are not trusted by browsers; users need a trusted
  certificate or a reverse proxy.
- A loopback bind without a token trusts every local process and every
  allowlisted origin. Documented, not mitigated.
- A persistent attacker that keeps about `max-clients + 16` connections in
  the header phase can hold every `LimitListener` slot, delaying other
  clients (including `/healthz`) by up to `ReadHeaderTimeout` (3s) per
  cycle. Established WebSockets are unaffected. Bounded, not eliminated.
