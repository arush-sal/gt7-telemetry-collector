package protocol

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
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

// TestDecodeGolden locks the full decoded struct for every fixture against a committed snapshot.
// Regenerate (after verifying the values are correct) with:  go test -run Golden -update
// Note: format-C golden snapshots are deferred to a future iteration when Decode handles C fields.
func TestDecodeGolden(t *testing.T) {
	// Glob all packet_*.bin and filter out the format-C prefix explicitly (format-C fixtures have
	// no golden snapshots yet), rather than relying on a numeric-suffix pattern that would
	// silently under-count a mis-named format-A fixture (e.g. packet_8.bin).
	allMatches, err := filepath.Glob(filepath.Join("testdata", "packet_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for _, m := range allMatches {
		if !strings.HasPrefix(filepath.Base(m), "packet_c_") {
			matches = append(matches, m)
		}
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
