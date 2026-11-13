# Security

## Token authentication

Ingest authentication is an optional bearer token. Set `GARAGE_INGEST_TOKEN` in
the collector's environment, or pass `--rtps-token` (a non-empty flag overrides
the environment variable). The consumer must validate the token to reject
unauthenticated telemetry; with no token configured the collector sends no
authentication header.

Prefer the environment variable: command-line arguments are visible in `ps`.
Tokens must match RFC 6750 `b64token` syntax, and they are redacted from
configuration error messages. HTTP(S) targets reject URL userinfo and query
strings so credentials cannot be smuggled into the URL.

## Plaintext transports

!!! warning
    gRPC uses plaintext transport. A bearer token sent over `http://`,
    `grpc://` or a bare `host:port` travels unencrypted.

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

## Signed releases, SBOM and provenance

Every release artifact (archives, `checksums.txt` and SBOMs) has a keyless
Sigstore bundle, and archives carry an SPDX SBOM. The release workflow also
publishes SLSA build provenance attestations. See
[Install](install.md#verify-a-release) for the `cosign verify-blob` and
`gh attestation verify` commands.

CI scans with Trivy, zizmor (GitHub Actions audit) and govulncheck; see
[Contributing](contributing.md#ci-workflows).

## Reporting a vulnerability

Please report vulnerabilities privately with GitHub private vulnerability
reporting (security advisories):
[report a vulnerability](https://github.com/arush-sal/gt7-telemetry-collector/security/advisories/new).
Do not open a public issue for security problems.
