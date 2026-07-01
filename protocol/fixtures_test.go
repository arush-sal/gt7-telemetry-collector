package protocol

import (
	"os"
	"path/filepath"
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

// TestFixturesPresent locks the fixture set: 8 encrypted packets, each the expected size.
func TestFixturesPresent(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "packet_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 8 {
		t.Fatalf("want 8 fixtures, got %d: %v", len(matches), matches)
	}
	for _, m := range matches {
		b := readFixture(t, filepath.Base(m))
		if len(b) != packetSize {
			t.Errorf("%s: want %d bytes, got %d", m, packetSize, len(b))
		}
	}
}
