package telemetry

import (
	"testing"
	"time"

	"github.com/arush-sal/garage/backend/collector/protocol"
	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
)

// TestToCanonical checks the GT7DData -> TelemetryFrame seam: capture metadata is carried through,
// narrow integers widen correctly (int16->int32, uint16/uint8->uint32), a set lap time becomes a
// Duration while an unset (-1) one becomes nil, floats pass through, and flags map (including the
// ASM/TCS acronym re-casing).
func TestToCanonical(t *testing.T) {
	now := time.Unix(1717, 0)
	d := protocol.GT7DData{
		PackageID:         42,
		CarID:             1984,
		CurrentLap:        2, // int16 -> int32
		TotalLaps:         3, // int16 -> int32
		BestLap:           90 * time.Second,
		LastLap:           -1 * time.Millisecond, // unset sentinel -> nil
		TimeOnTrack:       120 * time.Second,
		RPM:               7000,
		RPMRevWarning:     8000, // uint16 -> uint32
		EstimatedTopSpeed: 323,  // int16 -> int32
		CurrentGear:       4,    // uint8 -> uint32
		SuggestedGear:     5,
		CarSpeed:          214.5,
		Throttle:          100,
		InRace:            true,
		IsASMEngaged:      true,
		Gear1:             3.5,

		// Format-C tail
		WheelRotation:           1.5,
		SteeringAngularVelocity: 0.25,
		Sway:                    0.1,
		Heave:                   0.2,
		Surge:                   0.3,
		ThrottleFiltered:        99, // uint8 -> uint32
		BrakeFiltered:           88, // uint8 -> uint32
		UnknownU8_13E:           77, // uint8 -> uint32
		UnknownU8_13F:           66, // uint8 -> uint32
		TorqueVectors:           [4]float32{11, 22, 33, 44},
		EnergyRecovery:          5.5,
		UnknownF32_154:          6.6,
		SurfaceType:             [4]byte{'T', 'C', 'D', 'G'},
		CurrentLapTimeMs:        123456,
		WheelSteeringAngle:      [2]float32{0.05, -0.05},
		WheelBase:               2.5,
		CarCategory:             [4]byte{'G', 'R', 'N', 0},
	}
	m := CaptureMeta{RecvTime: now, Sequence: 7, Gap: true, Source: telemetryv1.Source_SOURCE_LIVE}

	f := ToCanonical(d, m)

	// Capture metadata
	if f.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", f.SchemaVersion, SchemaVersion)
	}
	if f.Sequence != 7 || !f.Gap || f.Source != telemetryv1.Source_SOURCE_LIVE {
		t.Errorf("metadata mismatch: seq=%d gap=%v source=%v", f.Sequence, f.Gap, f.Source)
	}
	if got := f.RecvTimestamp.AsTime(); !got.Equal(now) {
		t.Errorf("RecvTimestamp = %v, want %v", got, now)
	}

	// Integer widening
	if f.CurrentLap != 2 || f.TotalLaps != 3 || f.EstimatedTopSpeed != 323 {
		t.Errorf("int16->int32 widening wrong: lap=%d laps=%d top=%d", f.CurrentLap, f.TotalLaps, f.EstimatedTopSpeed)
	}
	if f.RpmRevWarning != 8000 {
		t.Errorf("uint16->uint32 wrong: %d", f.RpmRevWarning)
	}
	if f.CurrentGear != 4 || f.SuggestedGear != 5 {
		t.Errorf("uint8->uint32 wrong: gear=%d suggested=%d", f.CurrentGear, f.SuggestedGear)
	}
	if f.CarId != 1984 || f.PackageId != 42 {
		t.Errorf("id mismatch: car=%d pkg=%d", f.CarId, f.PackageId)
	}

	// Durations: set becomes a value, unset (-1) becomes nil
	if f.BestLap == nil || f.BestLap.AsDuration() != 90*time.Second {
		t.Errorf("BestLap = %v, want 90s", f.BestLap)
	}
	if f.LastLap != nil {
		t.Errorf("LastLap = %v, want nil (unset)", f.LastLap)
	}
	if f.TimeOnTrack == nil || f.TimeOnTrack.AsDuration() != 120*time.Second {
		t.Errorf("TimeOnTrack = %v, want 120s", f.TimeOnTrack)
	}

	// Float passthrough
	if f.CarSpeed != 214.5 || f.Throttle != 100 || f.Gear1 != 3.5 {
		t.Errorf("float mismatch: speed=%v throttle=%v gear1=%v", f.CarSpeed, f.Throttle, f.Gear1)
	}

	// Flags, including the ASM acronym re-cased to Asm in the generated field
	if !f.InRace || !f.IsAsmEngaged {
		t.Errorf("flags mismatch: inRace=%v asm=%v", f.InRace, f.IsAsmEngaged)
	}

	// Format-C tail: float passthrough
	if f.WheelRotation != 1.5 || f.SteeringAngularVelocity != 0.25 || f.Sway != 0.1 || f.Heave != 0.2 || f.Surge != 0.3 {
		t.Errorf("format-C motion floats mismatch: wheelRotation=%v steerAngVel=%v sway=%v heave=%v surge=%v",
			f.WheelRotation, f.SteeringAngularVelocity, f.Sway, f.Heave, f.Surge)
	}

	// Format-C tail: uint8 -> uint32 widening
	if f.ThrottleFiltered != 99 || f.BrakeFiltered != 88 || f.UnknownU8_13E != 77 || f.UnknownU8_13F != 66 {
		t.Errorf("format-C uint8->uint32 wrong: throttleFiltered=%d brakeFiltered=%d u13e=%d u13f=%d",
			f.ThrottleFiltered, f.BrakeFiltered, f.UnknownU8_13E, f.UnknownU8_13F)
	}

	// Format-C tail: per-wheel torque vectors (FL,FR,RL,RR)
	if f.TorqueVectorFl != 11 || f.TorqueVectorFr != 22 || f.TorqueVectorRl != 33 || f.TorqueVectorRr != 44 {
		t.Errorf("torque vector mismatch: fl=%v fr=%v rl=%v rr=%v", f.TorqueVectorFl, f.TorqueVectorFr, f.TorqueVectorRl, f.TorqueVectorRr)
	}

	// Format-C tail: remaining floats, raw lap time, and wheelbase
	if f.EnergyRecovery != 5.5 || f.UnknownF32_154 != 6.6 {
		t.Errorf("format-C misc floats mismatch: energyRecovery=%v unknownF32154=%v", f.EnergyRecovery, f.UnknownF32_154)
	}
	if f.CurrentLapTimeMs != 123456 {
		t.Errorf("CurrentLapTimeMs = %d, want 123456", f.CurrentLapTimeMs)
	}
	if f.WheelSteeringAngleFrontLeft != 0.05 || f.WheelSteeringAngleFrontRight != -0.05 {
		t.Errorf("wheel steering angle mismatch: left=%v right=%v", f.WheelSteeringAngleFrontLeft, f.WheelSteeringAngleFrontRight)
	}
	if f.WheelBase != 2.5 {
		t.Errorf("WheelBase = %v, want 2.5", f.WheelBase)
	}

	// Format-C tail: SurfaceType is a raw 4-char string (no NUL trimming), CarCategory is trimmed
	// at the first NUL byte.
	if f.SurfaceType != "TCDG" {
		t.Errorf("SurfaceType = %q, want %q", f.SurfaceType, "TCDG")
	}
	if f.CarCategory != "GRN" {
		t.Errorf("CarCategory = %q, want %q", f.CarCategory, "GRN")
	}
}

// TestToCanonicalCarName checks that the seam resolves car_id to a human-readable car_name, and
// that an unmapped id becomes "Unknown" rather than an empty string.
func TestToCanonicalCarName(t *testing.T) {
	m := CaptureMeta{Source: telemetryv1.Source_SOURCE_LIVE}

	// Known id: 104 has a unique base name, so its year is dropped.
	if f := ToCanonical(protocol.GT7DData{CarID: 104}, m); f.CarName != "Nissan Sileighty" {
		t.Errorf("CarName for id 104 = %q, want %q", f.CarName, "Nissan Sileighty")
	}
	// Unknown id maps to "Unknown", never blank.
	if f := ToCanonical(protocol.GT7DData{CarID: -1}, m); f.CarName != "Unknown" {
		t.Errorf("CarName for unknown id = %q, want %q", f.CarName, "Unknown")
	}
}
