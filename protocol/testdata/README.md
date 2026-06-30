# Test fixtures: encrypted GT7 packets

`packet_00.bin` .. `packet_07.bin` are **raw encrypted** GT7 telemetry UDP payloads
(296 bytes each), extracted from a real capture. They are still Salsa20-encrypted —
decrypting them is what `protocol.Decrypt` is tested against.

## Provenance

- Source: `supporting-data/nurburgring-lap.pcapng` (single Nürburgring lap, ~17 min, 60 Hz).
- Filter: UDP datagrams with destination port **33740** (the GT7 telemetry stream).
- Capture stats: 62,524 payloads on :33740, **all 296 bytes**; 619 heartbeat datagrams on :33739.
- Sampling: 8 payloads taken evenly across the capture (indices 0, 7815, ... 54705) to span a lap
  (start, braking zones, straights, etc.).
- Extracted with a throwaway gopacket tool (not part of the collector module).

## Notes

- Magic `0x47375330` is only visible **after** decryption (offset 0 of the plaintext).
- These are inputs for decrypt/decode tests; golden decoded values are snapshotted separately once
  `Decode` is verified.
