package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
)

type recPub struct{ frames []*telemetryv1.TelemetryFrame }

func (r *recPub) Publish(f *telemetryv1.TelemetryFrame) { r.frames = append(r.frames, f) }

func TestFanoutSamePointer(t *testing.T) {
	f := &telemetryv1.TelemetryFrame{Sequence: 7}
	fanout{}.Publish(f) // zero destinations: no panic

	one := &recPub{}
	fanout{one}.Publish(f)
	if len(one.frames) != 1 || one.frames[0] != f {
		t.Fatalf("one destination got %v", one.frames)
	}

	a, b := &recPub{}, &recPub{}
	fanout{a, b}.Publish(f)
	if len(a.frames) != 1 || len(b.frames) != 1 || a.frames[0] != f || b.frames[0] != f {
		t.Fatalf("two destinations got %v / %v", a.frames, b.frames)
	}
}

// TestFanoutPipelineOneDecode runs real encrypted fixtures through one pipeline feeding two sinks:
// each packet is decoded once and both sinks receive the identical frame pointer.
func TestFanoutPipelineOneDecode(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "protocol", "testdata", "packet_*.bin"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob fixtures: %v (%d)", err, len(paths))
	}
	a, b := &recPub{}, &recPub{}
	p := ingest.NewPipeline(nil, fanout{a, b})
	for i, path := range paths {
		pkt, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p.Handle(pkt, time.Unix(int64(i), 0))
	}
	n := p.Counters().Published
	if n == 0 || uint64(len(a.frames)) != n || uint64(len(b.frames)) != n {
		t.Fatalf("published %d, sinks got %d and %d", n, len(a.frames), len(b.frames))
	}
	for i := range a.frames {
		if a.frames[i] != b.frames[i] {
			t.Fatalf("frame %d: sinks got different pointers", i)
		}
	}
}
