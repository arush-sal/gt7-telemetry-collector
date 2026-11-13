# CLI reference

```sh
gt7-collector --ps5=<ip> [flags]
```

Flags are defined in `cmd/gt7-collector/main.go` (`parseFlags`) and validated
before any socket or file is opened. Positional arguments are rejected.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--ps5` | Required | Literal PS5 IP address (hostnames are rejected). |
| `--rtps` | `localhost:50051` | RTPS target: bare `host:port`, `grpc://host:port`, `unix:///abs/path.sock`, or `http(s)://host/path`. Must not be empty. See [Transports](transports.md). |
| `--rtps-token` | Empty (falls back to `GARAGE_INGEST_TOKEN`) | Optional ingest bearer token. Prefer the environment variable to keep it out of `ps`. |
| `--recv-port` | `33740` | Local UDP receive port; 1..65535. |
| `--send-port` | `33739` | Console UDP heartbeat destination port; 1..65535. |
| `--heartbeat` | `1s` | Heartbeat interval in Go duration syntax; must be positive. |
| `--rawdump-dir` | Empty (disabled) | Directory for the optional [raw dump](raw-dumps.md). |
| `--raw-buffer` | `1024` | Raw packet buffer depth; must be positive. |
| `--pub-buffer` | `1024` | Outbound frame buffer depth; must be positive. The oldest frame is dropped when full. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. Logs are JSON (zap production config). |
| `--version` | `false` | Print version information and exit. |

`--help` prints the flag list and exits 0.

## Environment

| Variable | Meaning |
| --- | --- |
| `GARAGE_INGEST_TOKEN` | Bearer token used when `--rtps-token` is empty. A non-empty flag overrides it. With neither set, no authentication header is sent. |

Tokens are redacted from configuration error messages.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Clean shutdown on SIGINT/SIGTERM, `--help`, or `--version`. |
| `1` | Startup or runtime failure (for example, the UDP port cannot be bound, an invalid target, or the raw sink cannot open). |
| `2` | Flag or configuration error, or logger construction failure. |

## Examples

```sh
# gRPC (plaintext) to a local RTPS
gt7-collector --ps5=192.168.1.50

# Unix socket, with raw capture and debug logs
gt7-collector --ps5=192.168.1.50 --rtps=unix:///run/garage/ingest.sock \
  --rawdump-dir=./rawdump --log-level=debug

# HTTPS with a token from the environment
GARAGE_INGEST_TOKEN=... gt7-collector --ps5=192.168.1.50 \
  --rtps=https://rtps.example.com/v1/ingest
```

The collector command has no replay flag; replay captures through the
[library](library.md#replay-a-capture) or the RTPS `--replay-file`.
