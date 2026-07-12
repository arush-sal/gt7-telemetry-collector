package protocol

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files from current decode output")

// identityPins are values cross-verified against an independent decoder's export of this exact
// session (2026-06-21 Nürburgring, car 1984). They pin the fields that have no natural bounds, so
// a wrong offset for any of them fails here against ground truth (not against our own output).
var identityPins = map[string]struct {
	packageID  uint32
	carID      int32
	currentLap int16
	totalLaps  int16
}{
	"packet_00.bin": {126297, 1984, 0, 2},
	"packet_01.bin": {134165, 1984, 1, 2},
	"packet_02.bin": {142033, 1984, 1, 2},
	"packet_03.bin": {149881, 1984, 1, 2},
	"packet_04.bin": {157733, 1984, 2, 2},
	"packet_05.bin": {165580, 1984, 2, 2},
	"packet_06.bin": {173432, 1984, 2, 2},
	"packet_07.bin": {181291, 1984, 2, 2},
}

// decodeFixture decrypts and decodes a fixture, failing the test on any error.
func decodeFixture(t *testing.T, name string) GT7DData {
	t.Helper()
	dec, err := Decrypt(readFixture(t, name))
	if err != nil {
		t.Fatalf("decrypt %s: %v", name, err)
	}
	d, err := Decode(dec)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return d
}

// TestDecodeIdentity checks the unbounded fields against externally-verified pins.
func TestDecodeIdentity(t *testing.T) {
	for name, want := range identityPins {
		t.Run(name, func(t *testing.T) {
			d := decodeFixture(t, name)
			if d.PackageID != want.packageID {
				t.Errorf("PackageID = %d, want %d", d.PackageID, want.packageID)
			}
			if d.CarID != want.carID {
				t.Errorf("CarID = %d, want %d", d.CarID, want.carID)
			}
			if d.CurrentLap != want.currentLap {
				t.Errorf("CurrentLap = %d, want %d", d.CurrentLap, want.currentLap)
			}
			if d.TotalLaps != want.totalLaps {
				t.Errorf("TotalLaps = %d, want %d", d.TotalLaps, want.totalLaps)
			}
		})
	}
}

// TestDecodeGolden locks the full decoded struct for every fixture — both format A (packet_*.bin)
// and format C (packet_c_*.bin) — against a committed snapshot. Now that Decode is format-aware,
// both families are covered by the same golden mechanism; format C simply carries more populated
// fields in its snapshot. Regenerate (after verifying the values are correct) with:
//
//	go test -run Golden -update
func TestDecodeGolden(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "packet_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		name := filepath.Base(m)
		t.Run(name, func(t *testing.T) {
			d := decodeFixture(t, name)
			got, err := json.MarshalIndent(d, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			golden := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: decode output does not match golden", name)
			}
		})
	}
}

// TestDecodeFormatCFields pins the format-C tail fields (0x128..0x170) against verified values
// from packet_c_00.bin (see the offset table's provenance comments in constants.go), and proves
// Decode is format-aware: a format-A fixture (packet_00.bin, 296 bytes) must decode with every one
// of these new fields left at its zero value, because format A never carries the C tail.
func TestDecodeFormatCFields(t *testing.T) {
	t.Run("packet_c_00.bin", func(t *testing.T) {
		d := decodeFixture(t, "packet_c_00.bin")

		if d.CurrentLapTimeMs != 1808 {
			t.Errorf("CurrentLapTimeMs = %d, want 1808", d.CurrentLapTimeMs)
		}
		wantSurface := [4]byte{'T', 'T', 'T', 'T'}
		if d.SurfaceType != wantSurface {
			t.Errorf("SurfaceType = %q, want %q", d.SurfaceType, wantSurface)
		}
		wantCategory := [4]byte{'G', 'R', 'N', 0}
		if d.CarCategory != wantCategory {
			t.Errorf("CarCategory = %q, want %q", d.CarCategory, wantCategory)
		}
		if d.WheelBase < 1.83 || d.WheelBase > 1.85 {
			t.Errorf("WheelBase = %v, want in [1.83, 1.85]", d.WheelBase)
		}
		if d.BrakeFiltered != 0 {
			t.Errorf("BrakeFiltered = %d, want 0", d.BrakeFiltered)
		}
	})

	t.Run("packet_00.bin format A tail is zero", func(t *testing.T) {
		d := decodeFixture(t, "packet_00.bin")

		var zero GT7DData
		if d.WheelRotation != zero.WheelRotation ||
			d.SteeringAngularVelocity != zero.SteeringAngularVelocity ||
			d.Sway != zero.Sway ||
			d.Heave != zero.Heave ||
			d.Surge != zero.Surge ||
			d.ThrottleFiltered != zero.ThrottleFiltered ||
			d.BrakeFiltered != zero.BrakeFiltered ||
			d.UnknownU8_13E != zero.UnknownU8_13E ||
			d.UnknownU8_13F != zero.UnknownU8_13F ||
			d.TorqueVectors != zero.TorqueVectors ||
			d.EnergyRecovery != zero.EnergyRecovery ||
			d.UnknownF32_154 != zero.UnknownF32_154 ||
			d.SurfaceType != zero.SurfaceType ||
			d.CurrentLapTimeMs != zero.CurrentLapTimeMs ||
			d.WheelSteeringAngle != zero.WheelSteeringAngle ||
			d.WheelBase != zero.WheelBase ||
			d.CarCategory != zero.CarCategory {
			t.Errorf("format-A packet populated a format-C-only field; want all zero, got %+v", d)
		}
	})
}
