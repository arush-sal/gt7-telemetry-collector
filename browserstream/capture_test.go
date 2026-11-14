package browserstream

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/replay"
	"go.uber.org/zap/zaptest/observer"
)

// Real GT7 captures live outside the repo and are never copied in; CI does not have them.

const gt7RecvPort = 33740

// realCaptures returns every *.pcapng in GT7_CAPTURE_DIR, or else in any
// supporting-data/raw-packet-captures or captures directory in the working directory or its
// ancestors (so it works from the main checkout and from .worktrees/<name>). Captures over 100MB
// run only without -short. It skips when none are found.
func realCaptures(t *testing.T) []string {
	t.Helper()
	dirs := []string{os.Getenv("GT7_CAPTURE_DIR")}
	if dirs[0] == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		dirs = nil
		for d := wd; ; d = filepath.Dir(d) {
			dirs = append(dirs, filepath.Join(d, "supporting-data", "raw-packet-captures"), filepath.Join(d, "captures"))
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	var paths []string
	seen := map[string]bool{}
	for _, d := range dirs {
		m, _ := filepath.Glob(filepath.Join(d, "*.pcapng"))
		for _, p := range m {
			real, err := filepath.EvalSymlinks(p)
			if err != nil || seen[real] {
				continue
			}
			seen[real] = true
			if fi, err := os.Stat(real); err == nil && fi.Mode().IsRegular() && (!testing.Short() || fi.Size() <= 100<<20) {
				paths = append(paths, real)
			}
		}
	}
	if len(paths) == 0 {
		t.Skip("no real GT7 captures found; set GT7_CAPTURE_DIR to a directory of *.pcapng")
	}
	return paths
}

// realCapture returns the named capture, skipping when it is not available.
func realCapture(t *testing.T, name string) string {
	t.Helper()
	for _, p := range realCaptures(t) {
		if filepath.Base(p) == name {
			return p
		}
	}
	t.Skipf("capture %s not found; set GT7_CAPTURE_DIR to the directory holding it", name)
	return ""
}

// play replays path at full speed into handle on the calling goroutine and returns the number of
// datagrams handed to handle.
func play(t *testing.T, path string, handle func([]byte, time.Time)) (packets uint64) {
	t.Helper()
	s, err := replay.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Play(context.Background(), gt7RecvPort, 1e9, func(b []byte, ts time.Time) {
		packets++
		handle(b, ts)
	}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	return packets
}

type pubFunc func(*telemetryv1.TelemetryFrame)

func (p pubFunc) Publish(f *telemetryv1.TelemetryFrame) { p(f) }

// liveStream runs a real hub behind a real Server and connects one real WebSocket client. The
// returned capture already holds hello(waiting) and is fed every later message until the test ends.
func liveStream(t *testing.T, timeout time.Duration) (*Hub, *capture) {
	t.Helper()
	h := newRealHub(t, 4096, timeout)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v", err)
		}
	})
	var logs *observer.ObservedLogs
	s, _ := listenServe(t, baseConfig(&logs), h)
	c, _, err := dialWS(t, "ws://"+s.Addr().String()+"/v1/stream", dialOpts(testOrigin, Protocol))
	if err != nil {
		t.Fatal(err)
	}
	hello := readMsg(t, c)
	wantHello(t, hello, StateWaiting)
	cp := &capture{lines: [][]byte{hello.raw}}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			_ = cp.send(context.Background(), b)
		}
	}()
	t.Cleanup(func() { _ = c.CloseNow(); <-readDone })
	return h, cp
}

// countStatus counts status messages with the given state and reason.
func countStatus(t *testing.T, lines [][]byte, st SourceState, reason string) int {
	t.Helper()
	n := 0
	for _, l := range lines {
		if m := decode(t, l); m.Type == "status" && m.State == st && m.Reason == reason {
			n++
		}
	}
	return n
}

// TestRealCapturesProject projects every frame of every real capture and checks the contract.
func TestRealCapturesProject(t *testing.T) {
	total := 0
	for _, path := range realCaptures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			sch := compileSchema(t)
			n := 0
			var last []byte
			p := ingest.NewPipeline(nil, pubFunc(func(f *telemetryv1.TelemetryFrame) {
				n++
				s, inv, err := Project(fixSession, f, 0)
				if err != nil || inv != 0 {
					t.Fatalf("frame %d: Project err=%v invalid=%d", f.Sequence, err, inv)
				}
				for _, v := range []struct {
					got  *float32
					want float32
				}{{s.Inputs.Brake, f.Brake}, {s.Inputs.Throttle, f.Throttle}} {
					if v.got == nil || *v.got < 0 || *v.got > 100 || *v.got != v.want {
						t.Fatalf("frame %d: input %v, want %v within [0,100]", f.Sequence, v.got, v.want)
					}
				}
				if s.PackageID != f.PackageId {
					t.Fatalf("frame %d: packageId %d, want %d", f.Sequence, s.PackageID, f.PackageId)
				}
				b := mustMarshal(t, s)
				if !bytes.Contains(b, []byte(`"steering":null`)) {
					t.Fatalf("frame %d: steering not null: %s", f.Sequence, b)
				}
				if n == 1 || n%97 == 0 {
					if err := validateBytes(t, sch, b); err != nil {
						t.Fatalf("frame %d fails schema: %v\n%s", f.Sequence, err, b)
					}
				}
				last = b
			}))
			packets := play(t, path, p.Handle)
			if err := validateBytes(t, sch, last); err != nil {
				t.Fatalf("last frame fails schema: %v\n%s", err, last)
			}
			c := p.Counters()
			if c.Valid == 0 || c.Valid != packets || c.Published != uint64(n) {
				t.Fatalf("counters %+v, published %d, packets %d", c, n, packets)
			}
			t.Logf("%d frames, counters %+v", n, c)
		})
		total++
	}
	t.Logf("%d captures", total)
}

// TestRealCaptureEndToEnd streams a whole real capture to a real WebSocket client and checks it
// receives every projected frame byte-for-byte, then source_stale.
func TestRealCaptureEndToEnd(t *testing.T) {
	path := realCapture(t, "type-c-capture.pcapng")
	// Long enough that the client always drains its backlog before stale (which discards it).
	h, cp := liveStream(t, 3*time.Second)
	var want [][]byte
	p := ingest.NewPipeline(nil, pubFunc(func(f *telemetryv1.TelemetryFrame) {
		s, _, err := Project(fixSession, f, 0)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, mustMarshal(t, s))
		h.Publish(f)
	}))
	play(t, path, p.Handle)
	n := len(want)
	if n != 2907 { // every datagram in this capture is a valid, in-order GT7 packet
		t.Fatalf("%d frames, want 2907 (and < ClientBuffer 4096)", n)
	}
	waitFor(t, "source_stale", func() bool { return cp.lastIs(t, func(m msg) bool { return m.State == StateStale }) })
	lines := cp.snapshot()
	if len(lines) != n+3 {
		t.Fatalf("client got %d messages, want hello + first_frame + %d samples + stale", len(lines), n)
	}
	sch := compileSchema(t)
	for i, l := range lines {
		if err := validateBytes(t, sch, l); err != nil {
			t.Fatalf("message %d fails schema: %v\n%s", i, err, l)
		}
	}
	checkOrder(t, lines)
	wantStatus(t, decode(t, lines[1]), StateLive, ReasonFirstFrame)
	for i, w := range want {
		wantSample(t, decode(t, lines[2+i]), uint64(i+1), 0)
		if !bytes.Equal(lines[2+i], w) {
			t.Fatalf("sample %d:\ngot  %s\nwant %s", i+1, lines[2+i], w)
		}
	}
	wantStatus(t, decode(t, lines[n+2]), StateStale, ReasonStaleTimeout)
	if s := h.Stats(); s.Dropped != 0 || s.InvalidInputs != 0 || s.ProjectErrors != 0 {
		t.Fatalf("hub stats %+v", s)
	}
	t.Logf("%d frames streamed", n)
}

// TestRealCapturePauseStaleResume replays a capture with a real home-button pause. Each >=1s
// capture gap is reproduced as a real-time pause of 3x the source timeout, which must produce
// source_stale then source_resumed; the end of the capture produces a final source_stale.
func TestRealCapturePauseStaleResume(t *testing.T) {
	path := realCapture(t, "daytona-2laps-pause-home-resume.pcapng")
	// ponytail: 1s (3s per pause), so a scheduler stall under -race on slow CI cannot fake a stale.
	const timeout = time.Second
	h, cp := liveStream(t, timeout)
	p := ingest.NewPipeline(nil, h)
	var prev time.Time
	gaps := 0
	play(t, path, func(b []byte, ts time.Time) {
		if !prev.IsZero() && ts.Sub(prev) >= time.Second {
			gaps++
			time.Sleep(3 * timeout)
		}
		prev = ts
		p.Handle(b, ts)
	})
	if gaps == 0 {
		t.Fatal("capture has no >=1s gap")
	}
	waitFor(t, "final source_stale", func() bool {
		return countStatus(t, cp.snapshot(), StateStale, ReasonStaleTimeout) == gaps+1
	})
	lines := cp.snapshot()
	checkOrder(t, lines) // alternation, strictly increasing sequence, droppedSincePrevious
	if st, re := countStatus(t, lines, StateStale, ReasonStaleTimeout), countStatus(t, lines, StateLive, ReasonResumed); st != gaps+1 || re != gaps {
		t.Fatalf("%d gaps: got %d stale / %d resumed, want %d / %d", gaps, st, re, gaps+1, gaps)
	}
	// Every resume is followed by at least one sample before the next stale.
	for i, l := range lines {
		if m := decode(t, l); m.Reason == ReasonResumed && (i+1 >= len(lines) || decode(t, lines[i+1]).Type != "sample") {
			t.Fatalf("resume at message %d not followed by a sample", i)
		}
	}
	if s := h.Stats(); s.InvalidInputs != 0 || s.ProjectErrors != 0 || s.SlowClosed != 0 {
		t.Fatalf("hub stats %+v", s)
	}
	t.Logf("%d gaps, %d messages, counters %+v", gaps, len(lines), p.Counters())
}
