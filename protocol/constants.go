package protocol

const (
	// packetSize is the fixed GT7 "A" telemetry packet length (bytes), confirmed from real captures:
	// every datagram on the telemetry port was exactly this size. Kept as the historical name for
	// format A; see packetSizeA/B/Tilde/C below for the full set of accepted lengths.
	packetSize = 296

	// magicNumber is the sentinel GT7 stamps at offset 0 of every packet, read as a little-endian
	// uint32 ("0S7G" in ASCII). A correct decryption reproduces it; a wrong key/nonce yields garbage
	// here instead, so it doubles as our "did decryption succeed?" check.
	magicNumber = 0x47375330

	// GT7's heartbeat character ("A", "B", "~", "C") selects one of four wire formats, each with its
	// own fixed packet length and its own IV XOR mask. All four are reverse-engineered facts (sizes
	// from real captures, masks from community/protocol research), not choices we made. The first
	// 0x128 bytes of every format share the format-A layout, so a single set of field offsets and a
	// single magic check work across all of them once the nonce is derived with the right mask.

	// packetSizeA is format A's length (bytes). EMPIRICALLY VERIFIED: existing format-A fixtures
	// decode correctly under ivMaskA.
	packetSizeA = packetSize
	// ivMaskA is format A's IV XOR mask. EMPIRICALLY VERIFIED against real format-A captures.
	ivMaskA = 0xDEADBEAF

	// packetSizeB is format B's length (bytes). DOC-REPORTED, UNVERIFIED: no format-B fixtures exist
	// in this repo to confirm it against a real capture.
	packetSizeB = 316
	// ivMaskB is format B's IV XOR mask. DOC-REPORTED, UNVERIFIED — taken from external protocol
	// documentation, never confirmed against a real format-B capture. Note it shares this exact
	// value with ivMaskC (both 0xDEADBEEF), and differs from ivMaskA by a single bit (BEAF vs BEEF):
	// this is intentional, not a typo — do not "fix" it to match ivMaskA.
	ivMaskB = 0xDEADBEEF

	// packetSizeTilde is format "~"'s length (bytes). DOC-REPORTED, UNVERIFIED: no format-~ fixtures
	// exist in this repo to confirm it against a real capture.
	packetSizeTilde = 344
	// ivMaskTilde is format "~"'s IV XOR mask. DOC-REPORTED, UNVERIFIED — taken from external
	// protocol documentation, never confirmed against a real format-~ capture.
	ivMaskTilde = 0x55FABB4F

	// packetSizeC is format C's length (bytes). EMPIRICALLY VERIFIED this session: 2907/2907 packets
	// in a real capture were exactly this size and decrypted correctly under ivMaskC.
	packetSizeC = 368
	// ivMaskC is format C's IV XOR mask. EMPIRICALLY VERIFIED this session against a real format-C
	// capture (2907/2907 packets decrypted correctly). It happens to equal ivMaskB (both
	// 0xDEADBEEF); that is a confirmed fact about the protocol, not a copy-paste slip.
	ivMaskC = 0xDEADBEEF

	// minDecodeLen is the smallest slice length Decode can safely read: one past the highest byte
	// offset any field touches (the end of the last field). Validate guards against it so Decode
	// never indexes out of bounds. It happens to equal packetSize today, but it is decode-derived,
	// not the wire size — decoupling them means a future packet-layout change can move one without
	// silently breaking the other.
	minDecodeLen = 0x128

	// resetThreshold is the backward PackageID jump (in packets) at or beyond which the Deduper
	// treats a lower id as a new-session reset rather than a stale/out-of-order packet. It sits in
	// the wide gap between realistic UDP reordering (a handful of packets at 60 Hz) and a real
	// counter reset (a drop from tens of thousands to ~0), so any value in roughly [100, 5000] works.
	resetThreshold = 1337

	// SendPort is the console UDP port the collector sends the heartbeat to (see HeartbeatPayload).
	SendPort = 33739
	// RecvPort is the local UDP port GT7 telemetry packets arrive on.
	RecvPort = 33740
)

// Byte offsets of each field within the DECRYPTED packet. Cross-verified against three independent
// GT7 implementations that agree exactly: snipem/go-gt7-telemetry (Go), gt7dashboard (Python), and
// Bornhall/gt7telemetry (Python). All multi-byte values are little-endian.
//
// Notes:
//   - 0x40 is the IV seed, consumed by Decrypt (nonce derivation), not decoded here.
//   - 0x28 is the rotation quaternion W component; we model rotation as 3 axes and skip it.
//   - Lap/position counters and lap times are SIGNED (int16/int32): BestLap/LastLap use -1 for
//     "unset", and pre-race positions use -1 after the start. Do not read them as unsigned.
//   - Tyre speed is derived, not stored: |3.6 * TyreDiameter * wheelAngularVelocity|.
const (
	// Motion (world space), all float32
	offPositionX = 0x04
	offPositionY = 0x08
	offPositionZ = 0x0C
	offVelocityX = 0x10
	offVelocityY = 0x14
	offVelocityZ = 0x18
	// rotation is a quaternion on the wire (X,Y,Z,W at 0x1C..0x28); we keep X,Y,Z as pitch/yaw/roll
	offRotationPitch = 0x1C
	offRotationYaw   = 0x20
	offRotationRoll  = 0x24
	offAngularVelX   = 0x2C
	offAngularVelY   = 0x30
	offAngularVelZ   = 0x34
	offRideHeight    = 0x38 // *1000 -> mm

	// Powertrain & fluids, all float32
	offRPM          = 0x3C
	offCurrentFuel  = 0x44
	offFuelCapacity = 0x48
	offCarSpeed     = 0x4C // *3.6 (m/s -> km/h)
	offBoost        = 0x50 // -1
	offOilPressure  = 0x54
	offWaterTemp    = 0x58
	offOilTemp      = 0x5C
	offTyreTempFL   = 0x60
	offTyreTempFR   = 0x64
	offTyreTempRL   = 0x68
	offTyreTempRR   = 0x6C

	// Identity, lap & race (SIGNED integers unless noted)
	offPackageID       = 0x70 // uint32 monotonic counter; also the dedup key
	offCurrentLap      = 0x74 // int16
	offTotalLaps       = 0x76 // int16
	offBestLap         = 0x78 // int32 ms, -1 = unset
	offLastLap         = 0x7C // int32 ms, -1 = unset
	offTimeOnTrack     = 0x80 // int32 ms
	offCurrentPosition = 0x84 // int16, -1 after start
	offTotalPositions  = 0x86 // int16, -1 after start
	offRPMRevWarning   = 0x88 // uint16
	offRPMRevLimiter   = 0x8A // uint16
	offEstimatedTop    = 0x8C // int16

	// Flags & controls
	offFlags0   = 0x8E // byte 0: bits 0-7 (InRace .. IsLightsOn)
	offFlags1   = 0x8F // byte 1: bits 0-3 (beams/ASM/TCS), bits 4-7 unknown
	offGear     = 0x90 // byte: low nibble = current gear, high nibble = suggested
	offThrottle = 0x91 // byte 0-255, /2.55 -> 0-100
	offBrake    = 0x92 // byte 0-255, /2.55 -> 0-100

	// Wheel angular velocity (rad/s), float32 — TyreSpeed derived from these + diameter
	offWheelOmegaFL = 0xA4
	offWheelOmegaFR = 0xA8
	offWheelOmegaRL = 0xAC
	offWheelOmegaRR = 0xB0

	// Tyre diameter (m) & suspension, all float32
	offTyreDiameterFL = 0xB4
	offTyreDiameterFR = 0xB8
	offTyreDiameterRL = 0xBC
	offTyreDiameterRR = 0xC0
	offSuspensionFL   = 0xC4
	offSuspensionFR   = 0xC8
	offSuspensionRL   = 0xCC
	offSuspensionRR   = 0xD0

	// Clutch & transmission, all float32
	offClutch         = 0xF4
	offClutchEngaged  = 0xF8
	offRPMAfterClutch = 0xFC
	offGear1          = 0x104
	offGear2          = 0x108
	offGear3          = 0x10C
	offGear4          = 0x110
	offGear5          = 0x114
	offGear6          = 0x118
	offGear7          = 0x11C
	offGear8          = 0x120

	offCarID = 0x124 // int32
)
