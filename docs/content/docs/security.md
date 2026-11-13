---
title: Security
weight: 9
---

## Token authentication

Ingest authentication is an optional bearer token. Set `INGEST_TOKEN` in
the collector's environment, or pass `--rtps-token` (a non-empty flag overrides
the environment variable). The consumer must validate the token to reject
unauthenticated telemetry; with no token configured the collector sends no
authentication header.

Prefer the environment variable: command-line arguments are visible in `ps`.
Tokens must match RFC 6750 `b64token` syntax, and they are redacted from
configuration error messages. HTTP(S) targets reject URL userinfo and query
strings so credentials cannot be smuggled into the URL.

## Plaintext transports

{{< callout type="warning" >}}
gRPC uses plaintext transport. A bearer token sent over `http://`,
`grpc://` or a bare `host:port` travels unencrypted.
{{< /callout >}}

The collector logs a startup warning when a token is used with one of those
targets. Use HTTPS when the token must be encrypted in transit.

## Unix sockets

`unix:///path.sock` targets ignore the bearer token and rely on socket file
permissions. The consumer creates the socket; restrict it (for example mode
`0600`, or a directory only the intended users can enter) so that only the
collector's user can connect. No warning is logged for Unix targets.

## CORS is not authentication

If your consumer exposes an HTTP ingest endpoint, CORS headers only control what
browsers may read cross-origin. They do not authenticate the collector. Always
validate the bearer token on the server.

## Raw dumps

Raw dumps contain the still-encrypted packets, but the GT7 key is public, so
anyone with a dump can decrypt it. Treat dump files as plain telemetry data.

## Browser stream

The optional WebSocket listener (`--ws-listen`) is off by default. When enabled
it is read-only and serves brake and throttle only. Full model:
[Browser streaming]({{< relref "browser-streaming" >}}) and
[ADR 0003](https://github.com/arush-sal/gt7-telemetry-collector/blob/master/docs/adr/0003-websocket-threat-model.md).

- **Bind:** the host must be an IP literal or `localhost`. Loopback is the safe
  default; `:port`, `0.0.0.0`, `::` and other non-loopback IPs require
  `GT7_WS_TOKEN` and either `--ws-tls-cert`/`--ws-tls-key` or the explicit
  `--ws-allow-insecure-lan` (which logs a plaintext warning).
- **Origin:** `--ws-origin` is mandatory and matched exactly (no wildcards).
  A missing, `null` or unlisted `Origin` gets HTTP 403 before the upgrade.
  Browsers do not apply same-origin policy to WebSockets, so this check is the
  protection against hostile websites.
- **Host:** on loopback binds, any `Host` other than a loopback IP literal or
  `localhost` gets HTTP 421 (DNS rebinding), on every path.
- **Token:** `GT7_WS_TOKEN` (environment only, 32..256 characters from
  `A-Za-z0-9._~-`; generate with `openssl rand -hex 32`). Browsers send it as a
  second subprotocol, `["gt7.inputs.v1", "gt7.token." + token]`. It is never
  accepted in the URL, never echoed and compared in constant time. It is
  redacted from configuration and runtime errors, and `Sec-WebSocket-Protocol`
  is never logged. The token is visible to devtools on the page and to any
  TLS-terminating proxy; it grants read-only access to input telemetry.
- **TLS:** Go default TLS config (minimum TLS 1.2). Browsers do not trust
  self-signed certificates. A proxy that terminates TLS must not log
  `Sec-WebSocket-Protocol`.
- **Limits:** about 8 KiB of headers (`net/http` adds a 4 KiB allowance), 3s header timeout, no request bodies, 4 clients
  by default (max 64) with slots taken only after Origin, Host and token
  pass, bounded per-client queues and write deadlines, 1 KiB read limit (any
  data message closes the connection with 1008), ping every 10s.
- **No CORS headers**, no access log, and `/healthz` reveals nothing beyond
  `{"status":"ok"}`.

A loopback bind without a token trusts every local process and every
allowlisted origin.

## Signed releases, SBOM and provenance

Every release artifact (archives, `checksums.txt` and SBOMs) has a keyless
Sigstore bundle, and archives carry an SPDX SBOM. The release workflow also
publishes SLSA build provenance attestations. See
[Install]({{< relref "install#verify-a-release" >}}) for the `cosign verify-blob` and
`gh attestation verify` commands.

CI scans with Trivy, zizmor (GitHub Actions audit) and govulncheck; see
[Contributing]({{< relref "contributing#ci-workflows" >}}).

## Reporting a vulnerability

Please report vulnerabilities privately with GitHub private vulnerability
reporting (security advisories):
[report a vulnerability](https://github.com/arush-sal/gt7-telemetry-collector/security/advisories/new).
Do not open a public issue for security problems.
