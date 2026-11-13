---
title: Protocol
weight: 7
---

These are reverse-engineered facts about the GT7 telemetry protocol, as
implemented in the `protocol` package. They are not an official specification.
See `protocol/constants.go` for provenance and per-field confidence notes.

## Ports and heartbeat

| Item | Value |
| --- | --- |
| Telemetry receive port (local UDP) | `33740` (`protocol.RecvPort`) |
| Heartbeat destination port (on the console) | `33739` (`protocol.SendPort`) |
| Heartbeat payload | the single byte `"C"` (`protocol.HeartbeatPayload`) |

The receiver sends a heartbeat immediately and then every `--heartbeat`
interval, from the same socket it receives on. The heartbeat byte selects the
packet format the console streams back. The collector requests `"C"`, the
largest format, because the decoder reads the region shared by all formats.
`"A"` selects the standard format but is not what this collector sends. Both
ports can be overridden with `--recv-port` and `--send-port`.

## Packet formats

Every format is identified by its datagram length. Each has its own IV XOR mask.
All multi-byte fields are little-endian.

| Heartbeat | Length (bytes) | IV XOR mask | Status |
| --- | --- | --- | --- |
| `A` | 296 | `0xDEADBEAF` | Verified against real captures |
| `B` | 316 | `0xDEADBEEF` | Documented, unverified (no fixtures) |
| `~` | 344 | `0x55FABB4F` | Documented, unverified (no fixtures) |
| `C` | 368 | `0xDEADBEEF` | Verified against real captures |

The first `0x128` bytes of every format share the format-A layout, so one set of
field offsets works across all four. Format C adds a tail (`0x128`..`0x170`)
with fields such as wheel rotation, sway/heave/surge, surface type, wheel
steering angle, wheel base and car category. Some tail fields are marked
medium-confidence or unknown in the source.

## Encryption

Packets are encrypted with Salsa20 under a fixed 32-byte key: the first 32 bytes
of the ASCII string `Simulator Interface Packet GT7 ver 0.0`.

The per-packet nonce is derived from an IV word stored in clear at offset
`0x40`:

- `iv1` = little-endian uint32 at `0x40`
- `iv2` = `iv1` XOR the format's mask
- 8-byte nonce = `iv2` (little-endian, low 4 bytes) followed by `iv1`
  (little-endian, high 4 bytes)

## Magic

After decryption, the little-endian uint32 at offset 0 must equal `0x47375330`
(`"0S7G"` in ASCII). This doubles as the "did decryption succeed" check: a wrong
key or nonce yields garbage there. `protocol.Decrypt` returns `ErrBadMagic` on a
mismatch and `ErrPacketLength` for an unrecognized length.

## Deduplication

Each packet carries a monotonic `PackageID` (uint32 at `0x70`). The pipeline
keeps a packet when its ID advances, drops duplicates and small reorders (a
backward jump below 1337), and treats a larger backward jump as a new-session
reset. A forward jump of more than one is recorded as a gap on the frame.

## Field notes

- Lap and position counters and lap times are signed; `-1` means unset, which
  the canonical frame represents as an unset duration.
- Car speed is `m/s * 3.6` (km/h); ride height is scaled to millimeters.
- The gear byte holds the current gear in its low nibble and the suggested gear
  in its high nibble.
- Throttle and brake are bytes `0..255`, scaled to `0..100`.
- Tyre speed is derived from wheel angular velocity and tyre diameter.
