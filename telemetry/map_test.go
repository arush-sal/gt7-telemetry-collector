package telemetry

import (
	"testing"
	"time"

	"github.com/arush-sal/gt7/backend/collector/protocol"
	telemetryv1 "github.com/arush-sal/gt7/backend/gen/gt7/telemetry/v1"
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
}
