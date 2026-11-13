---
title: CLI reference
weight: 3
---

```sh
gt7-collector --ps5=<ip> [flags]
```

Flags are defined in `cmd/gt7-collector/main.go` (`parseFlags`) and validated
before any socket or file is opened. Positional arguments are rejected.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--ps5` | Required | Literal PS5 IP address (hostnames are rejected). |
| `--rtps` | `localhost:50051` | RTPS target: bare `host:port`, `grpc://host:port`, `unix:///abs/path.sock`, or `http(s)://host/path`. Must not be empty. `none` disables RTPS publishing and requires `--ws-listen` (with `none`, `--rtps-token` is rejected and `INGEST_TOKEN` is ignored). See [Transports]({{< relref "transports" >}}). |
| `--rtps-token` | Empty (falls back to `INGEST_TOKEN`) | Optional ingest bearer token. Prefer the environment variable to keep it out of `ps`. |
| `--recv-port` | `33740` | Local UDP receive port; 1..65535. |
| `--send-port` | `33739` | Console UDP heartbeat destination port; 1..65535. |
| `--heartbeat` | `1s` | Heartbeat interval in Go duration syntax; must be positive. |
| `--rawdump-dir` | Empty (disabled) | Directory for the optional [raw dump]({{< relref "raw-dumps" >}}). |
| `--raw-buffer` | `1024` | Raw packet buffer depth; must be positive. |
| `--pub-buffer` | `1024` | Outbound frame buffer depth; must be positive. The oldest frame is dropped when full. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. Logs are JSON (zap production config). |
| `--version` | `false` | Print version information and exit. |
| `--ws-listen` | Empty (disabled) | Browser WebSocket listener `host:port`. Host must be an IP literal or `localhost` (bound as `127.0.0.1`); other hostnames are rejected. Empty host, `0.0.0.0`, `::` or any non-loopback IP is a non-loopback bind. |
| `--ws-origin` | None | Allowed browser origin `scheme://host[:port]` (`http`/`https`, no path, no wildcard, not `null`). Repeat the flag for more origins; values are never split on commas. At least one is required with `--ws-listen`. |
| `--ws-max-clients` | `4` | Concurrent WebSocket clients; 1..64. |
| `--ws-client-buffer` | `64` | Per-client sample queue depth (oldest dropped when full); 1..4096. |
| `--ws-write-timeout` | `2s` | Per-message write deadline; 100ms..60s. |
| `--ws-source-timeout` | `2s` | No-frame interval before `source_stale`; 100ms..60s. |
| `--ws-allow-insecure-lan` | `false` | Permit plaintext `ws://` on a non-loopback `--ws-listen`. |
| `--ws-tls-cert`, `--ws-tls-key` | Empty | Serve `wss://` with this certificate and key; set both or neither. Files are loaded at startup (a load failure exits 1). |

Every `--ws-*` flag other than `--ws-listen` requires `--ws-listen`. A
non-loopback `--ws-listen` requires `GT7_WS_TOKEN` and either TLS or
`--ws-allow-insecure-lan`.

`--help` prints the flag list and exits 0.

## Environment

| Variable | Meaning |
| --- | --- |
| `INGEST_TOKEN` | Bearer token used when `--rtps-token` is empty. A non-empty flag overrides it. With neither set, no authentication header is sent. Ignored with `--rtps=none`. |
| `GT7_WS_TOKEN` | Browser stream access token (32..256 characters of `A-Z a-z 0-9 . _ ~ -`, e.g. `openssl rand -hex 32`). Required for a non-loopback `--ws-listen`; enforced on loopback when set. There is no flag. Ignored without `--ws-listen`. |

Tokens (`INGEST_TOKEN`, `--rtps-token` and `GT7_WS_TOKEN`) are redacted, raw or quoted, from configuration and runtime error messages, and are never logged.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Clean shutdown on SIGINT/SIGTERM, `--help`, or `--version`. |
| `1` | Startup or runtime failure (for example, the UDP port cannot be bound, an invalid target, or the raw sink cannot open, the `--ws-listen` port cannot be bound, or the TLS files cannot be loaded). |
| `2` | Flag or configuration error, or logger construction failure. |

## Examples

```sh
# gRPC (plaintext) to a local RTPS
gt7-collector --ps5=192.168.1.50

# Unix socket, with raw capture and debug logs
gt7-collector --ps5=192.168.1.50 --rtps=unix:///run/gt-collector/ingest.sock \
  --rawdump-dir=./rawdump --log-level=debug

# HTTPS with a token from the environment
INGEST_TOKEN=... gt7-collector --ps5=192.168.1.50 \
  --rtps=https://rtps.example.com/v1/ingest
```

The collector command has no replay flag; replay captures through the
[library]({{< relref "library#replay-a-capture" >}}) or the RTPS `--replay-file`.
