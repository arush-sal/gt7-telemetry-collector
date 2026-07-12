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

	// Format-C tail (0x128..0x170). Present only in the 368-byte format-C packet (packetSizeC);
	// format-A packets end at 0x128 and never carry these bytes. Reverse-engineered protocol fact,
	// cross-checked against our own real format-C captures where noted below. Confidence levels: HIGH
	// = confirmed against video/on-screen ground truth or a known real-world constant (e.g. a car's
	// actual wheelbase); MED = plausible and internally consistent but not independently confirmed;
	// UNCONFIRMED = read-but-untested (e.g. always 0 in our capture, which is also consistent with
	// the car/situation never exercising the field); UNKNOWN = meaning not identified at all.
	offWheelRotation = 0x128 // float32, rad. MED: read exactly pi during peak steering in our
	// capture, which is suspicious (could be coincidence, not confirmation) — treat as unconfirmed
	// in spirit despite the exact match.
	offSteeringAngularVelocity = 0x12C // float32, rad/s. MED (reverse-engineered; not independently confirmed).
	offSway                    = 0x130 // float32, X-axis acceleration. MED (reverse-engineered).
	offHeave                   = 0x134 // float32, Y-axis acceleration. MED (reverse-engineered).
	offSurge                   = 0x138 // float32, Z-axis acceleration. MED (reverse-engineered).

	offThrottleFiltered = 0x13C // uint8, 0-255. HIGH: tracks the throttle trace in our reference video.
	offBrakeFiltered    = 0x13D // uint8, 0-255. UNCONFIRMED: constant 0 throughout our capture, but
	// so is the raw brake byte (offBrake) — the capture never brakes, so this doesn't confirm the
	// offset, only that it's consistent with "no braking."
	offUnknownU8_13E = 0x13E // uint8. UNKNOWN: constant 0 in our capture, meaning not identified.
	offUnknownU8_13F = 0x13F // uint8. UNKNOWN: constant 0 in our capture, meaning not identified.

	// Torque vectors, one float32 per wheel (FL,FR,RL,RR order per the reverse-engineered field map), +drive/-brake·regen.
	// UNCONFIRMED: constant 0 in our capture (front-wheel-drive, non-hybrid car — a genuinely
	// zero-torque-vectoring car would also read 0 here, so this doesn't confirm the offsets).
	offTorqueVectorFL = 0x140
	offTorqueVectorFR = 0x144
	offTorqueVectorRL = 0x148
	offTorqueVectorRR = 0x14C

	offEnergyRecovery = 0x150 // float32, battery regen. UNCONFIRMED: constant 0 (non-hybrid car in
	// our capture; a hybrid car would be needed to confirm this offset actually carries regen data).
	offUnknownF32_154 = 0x154 // float32. UNKNOWN/under-research: varies 0.02-0.79 across our
	// capture with no identified correlate; flagged for future investigation, not decoded meaning.

	// SurfaceType: one byte per wheel (FL,FR,RL,RR), a surface-material code. HIGH: codes observed
	// match the track surface under each wheel in our capture and the documented code table (see
	// vault provenance) (T=tarmac, C=curb, D=dirt, G=grass, S=sand, s=snow).
	offSurfaceType = 0x158 // [4]byte

	offCurrentLapTimeMs = 0x15C // int32, ms. HIGH: exact match to the on-screen lap clock in our
	// reference video. Renamed from the reverse-engineered field name to avoid confusion with the
	// existing offCurrentLap (lap *number*, at 0x74) — this field is a lap *time*.

	// WheelSteeringAngle: per-wheel steering angle (rad), [2]float32 = {left, right} per the
	// reverse-engineered field map (only front wheels steer). HIGH: peaks during cornering in our
	// capture, consistent with steering input.
	offWheelSteeringAngleL = 0x160
	offWheelSteeringAngleR = 0x164

	offWheelBase = 0x168 // float32, m. HIGH: read ~1.8405 m in our capture, matching the real-world
	// wheelbase of a Fiat Abarth 595 (1.843 m) closely enough to confirm the offset.

	// CarCategory: a null-terminated ASCII category code, [4]byte. HIGH: our capture reads
	// {'G','R','N',0} ("GRN"), matching GT7's in-game "N-class" category naming convention per the
	// documented code table (see vault provenance).
	offCarCategory = 0x16C
)
