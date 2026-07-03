package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7/backend/gen/gt7/telemetry/v1"
)

// fakePub records published frames so tests can assert on them without gRPC.
type fakePub struct{ frames []*telemetryv1.TelemetryFrame }

func (f *fakePub) Publish(fr *telemetryv1.TelemetryFrame) { f.frames = append(f.frames, fr) }

// readFix loads an encrypted packet fixture from the protocol package's testdata.
func readFix(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestPipelineHappyPath feeds the eight real fixtures through the pipeline and asserts each decodes,
// maps, and publishes in order with a monotonic sequence. The fixtures are sampled far apart in the
// capture, so every packet after the first is legitimately preceded by a PackageID gap.
func TestPipelineHappyPath(t *testing.T) {
	pub := &fakePub{}
	p := NewPipeline(nil, pub)

	fixtures, err := filepath.Glob(filepath.Join("..", "protocol", "testdata", "packet_*.bin"))
	if err != nil || len(fixtures) != 8 {
		t.Fatalf("want 8 fixtures, got %d (err %v)", len(fixtures), err)
	}
	for i, f := range fixtures {
		p.Handle(readFix(t, filepath.Base(f)), time.Unix(int64(i), 0))
	}

	c := p.Counters()
	if c.Published != 8 || len(pub.frames) != 8 {
		t.Fatalf("published %d frames (counter %d), want 8", len(pub.frames), c.Published)
	}
	if c.Foreign != 0 || c.Short != 0 || c.Stale != 0 || c.Resets != 0 {
		t.Errorf("unexpected drops: %+v", c)
	}
	if c.Gaps != 7 {
		t.Errorf("Gaps = %d, want 7 (fixtures sampled far apart)", c.Gaps)
	}
	// First frame: cross-verified identity, sequence 1, no gap (cold start).
	if f0 := pub.frames[0]; f0.CarId != 1984 || f0.Sequence != 1 || f0.Gap {
		t.Errorf("frame0 = {car %d seq %d gap %v}, want {1984 1 false}", f0.CarId, f0.Sequence, f0.Gap)
	}
	if pub.frames[1].Gap != true {
		t.Error("frame1 should be flagged as a gap")
	}
}

// TestPipelineDropsDuplicate feeds the same packet twice; the second is a duplicate and must be
// dropped as stale, not published.
func TestPipelineDropsDuplicate(t *testing.T) {
	pub := &fakePub{}
	p := NewPipeline(nil, pub)

	pkt := readFix(t, "packet_00.bin")
	p.Handle(pkt, time.Unix(0, 0))
	p.Handle(pkt, time.Unix(1, 0))

	c := p.Counters()
	if c.Published != 1 || len(pub.frames) != 1 {
		t.Errorf("published %d, want 1", len(pub.frames))
	}
	if c.Stale != 1 {
		t.Errorf("Stale = %d, want 1", c.Stale)
	}
}

// TestPipelineDropsForeign feeds bytes that are not a valid GT7 packet; decryption fails the magic
// check, so the packet is counted foreign and nothing is published.
func TestPipelineDropsForeign(t *testing.T) {
	pub := &fakePub{}
	p := NewPipeline(nil, pub)

	p.Handle(make([]byte, 296), time.Unix(0, 0)) // all zeros -> wrong magic after decrypt

	c := p.Counters()
	if c.Foreign != 1 {
		t.Errorf("Foreign = %d, want 1", c.Foreign)
	}
	if len(pub.frames) != 0 {
		t.Errorf("published %d frames, want 0", len(pub.frames))
	}
}
