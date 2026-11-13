# Install

## GitHub Releases

Prebuilt binaries for linux, macOS and windows (amd64, arm64) are attached to
each [GitHub Release](https://github.com/arush-sal/gt7-telemetry-collector/releases).
Archives are named `gt7-collector_<version>_<os>_<arch>` (`.tar.gz`, or `.zip`
on windows) and contain the binary, `LICENSE` and `README.md`. Each release also
carries `checksums.txt` (sha256), an SPDX SBOM per archive, and a Sigstore
bundle (`.sigstore.json`) for every archive, the checksum file and each SBOM.

## go install

Requires **Go 1.27.4 or later**, as specified in `go.mod`.

```sh
go install github.com/arush-sal/gt7-telemetry-collector/cmd/gt7-collector@latest
gt7-collector --version
```

`--version` prints `gt7-collector <version> (commit <commit>, built <date>)`.
A `go install` build reports `dev`, `none` and `unknown` because the release
values are injected only by GoReleaser.

## Verify a release

Download an artifact `X` and its `X.sigstore.json` bundle, then check the keyless
signature and the SLSA build provenance:

```sh
cosign verify-blob --bundle X.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/arush-sal/gt7-telemetry-collector/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com X

gh attestation verify X --repo arush-sal/gt7-telemetry-collector

sha256sum -c --ignore-missing checksums.txt
```

See [Security](security.md) for what each check proves.
