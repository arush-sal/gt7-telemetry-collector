package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture loads an encrypted packet fixture from testdata by base name (e.g. "packet_00.bin").
// Reuse this in decrypt/decode tests.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestFixturesPresent locks the fixture set: 8 format-A encrypted packets (packetSize bytes each)
// and 8 format-C encrypted packets (368 bytes each, see packet_c_*.bin).
func TestFixturesPresent(t *testing.T) {
	// Glob all packet_*.bin and filter out the format-C prefix explicitly, rather than relying on
	// a numeric-suffix pattern that would silently under-count a mis-named format-A fixture (e.g.
	// packet_8.bin).
	allMatches, err := filepath.Glob(filepath.Join("testdata", "packet_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var aMatches []string
	for _, m := range allMatches {
		if !strings.HasPrefix(filepath.Base(m), "packet_c_") {
			aMatches = append(aMatches, m)
		}
	}
	if len(aMatches) != 8 {
		t.Fatalf("want 8 format-A fixtures, got %d: %v", len(aMatches), aMatches)
	}
	for _, m := range aMatches {
		b := readFixture(t, filepath.Base(m))
		if len(b) != packetSize {
			t.Errorf("%s: want %d bytes, got %d", m, packetSize, len(b))
		}
	}

	cMatches, err := filepath.Glob(filepath.Join("testdata", "packet_c_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cMatches) != 8 {
		t.Fatalf("want 8 format-C fixtures, got %d: %v", len(cMatches), cMatches)
	}
	const packetSizeC = 368
	for _, m := range cMatches {
		b := readFixture(t, filepath.Base(m))
		if len(b) != packetSizeC {
			t.Errorf("%s: want %d bytes, got %d", m, packetSizeC, len(b))
		}
	}
}
