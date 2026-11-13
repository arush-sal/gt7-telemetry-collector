package browserstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "regenerate testdata/v1/transcripts/*.jsonl")

// fakeTimeout is the source timeout for fake-clock hubs; tests advance the clock past it and call
// checkStale directly, so no real time passes.
const fakeTimeout = time.Second

type sendFunc = func(context.Context, []byte) error

// msg is a decoded server message (union of the fields the tests inspect).
type msg struct {
	Type    string      `json:"type"`
	Status  SourceState `json:"status"`
	State   SourceState `json:"state"`
	Reason  string      `json:"reason"`
	Seq     string      `json:"sequence"`
	Dropped uint64      `json:"droppedSincePrevious"`
	Inputs  struct {
		Brake *float64 `json:"brake"`
	} `json:"inputs"`
	raw []byte
}

func (m msg) seq(t *testing.T) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(m.Seq, 10, 64)
	if err != nil {
		t.Fatalf("bad sequence in %s", m.raw)
	}
	return n
}

func decode(t *testing.T, b []byte) msg {
	t.Helper()
	var m msg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	m.raw = b
	return m
}

// newFakeHub returns a hub whose monotonic clock is controlled by the returned counter.
func newFakeHub(t *testing.T, buf int) (*Hub, *atomic.Int64) {
	t.Helper()
	h, err := NewHub(HubConfig{SessionID: fixSession, CollectorVersion: fixVersion, ClientBuffer: buf, SourceTimeout: fakeTimeout})
	if err != nil {
		t.Fatal(err)
	}
	clk := new(atomic.Int64)
	h.now = func() time.Duration { return time.Duration(clk.Load()) }
	return h, clk
}

func newRealHub(t *testing.T, buf int, timeout time.Duration) *Hub {
	t.Helper()
	h, err := NewHub(HubConfig{SessionID: fixSession, CollectorVersion: fixVersion, ClientBuffer: buf, SourceTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// goStale advances the fake clock past the timeout and runs one monitor check.
func goStale(h *Hub, clk *atomic.Int64) {
	clk.Add(int64(fakeTimeout))
	h.checkStale()
}

type conn struct {
	c      *client
	out    chan []byte   // pipe conns only
	sends  *atomic.Int64 // pipe conns only: send calls entered (the writer may be blocked in one)
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// dial registers a client and runs its writer with send until the test ends.
func dial(t *testing.T, h *Hub, send sendFunc, writeTimeout time.Duration) *conn {
	t.Helper()
	c, ok := h.register()
	if !ok {
		t.Fatal("register refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cn := &conn{c: c, cancel: cancel, done: make(chan struct{})}
	go func() { cn.err = h.stream(ctx, c, send, writeTimeout); close(cn.done) }()
	t.Cleanup(func() { cancel(); <-cn.done })
	return cn
}

// pipeConn is a client whose writer blocks in send until the test reads the message with next.
func pipeConn(t *testing.T, h *Hub) *conn {
	t.Helper()
	out := make(chan []byte)
	sends := new(atomic.Int64)
	cn := dial(t, h, func(ctx context.Context, b []byte) error {
		sends.Add(1)
		select {
		case out <- b:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, time.Minute)
	cn.out, cn.sends = out, sends
	return cn
}

func (cn *conn) next(t *testing.T) msg {
	t.Helper()
	select {
	case b := <-cn.out:
		return decode(t, b)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a message")
		return msg{}
	}
}

// waitFor polls cond for up to 5s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitDequeued waits until the writer has taken every queued sample (it is then blocked in send).
func waitDequeued(t *testing.T, cn *conn) {
	t.Helper()
	waitFor(t, "writer to dequeue", func() bool { return len(cn.c.samples) == 0 })
}

func wantHello(t *testing.T, m msg, st SourceState) {
	t.Helper()
	if m.Type != "hello" || m.Status != st {
		t.Fatalf("want hello %s, got %s", st, m.raw)
	}
}

func wantStatus(t *testing.T, m msg, st SourceState, reason string) {
	t.Helper()
	if m.Type != "status" || m.State != st || m.Reason != reason {
		t.Fatalf("want status %s/%s, got %s", st, reason, m.raw)
	}
}

func wantSample(t *testing.T, m msg, seq, dropped uint64) {
	t.Helper()
	if m.Type != "sample" || m.seq(t) != seq || m.Dropped != dropped {
		t.Fatalf("want sample seq=%d dropped=%d, got %s", seq, dropped, m.raw)
	}
}

func TestNewHubValidates(t *testing.T) {
	ok := HubConfig{SessionID: fixSession, ClientBuffer: 1, SourceTimeout: time.Second}
	if _, err := NewHub(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mut := range map[string]func(*HubConfig){
		"no session":   func(c *HubConfig) { c.SessionID = "" },
		"zero buffer":  func(c *HubConfig) { c.ClientBuffer = 0 },
		"zero timeout": func(c *HubConfig) { c.SourceTimeout = 0 },
	} {
		c := ok
		mut(&c)
		if _, err := NewHub(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPublishNoClientsZeroAllocs(t *testing.T) {
	h, _ := newFakeHub(t, 4)
	if st := h.Stats().State; st != StateWaiting {
		t.Fatalf("initial state %s", st)
	}
	f := frame(1, 10, 20)
	if a := testing.AllocsPerRun(1000, func() { h.Publish(f) }); a != 0 {
		t.Fatalf("Publish allocs/op = %v, want 0", a)
	}
	if st := h.Stats().State; st != StateLive {
		t.Fatalf("state after publish %s", st)
	}
}

func TestPublishFourClientsZeroAllocs(t *testing.T) {
	h, _ := newFakeHub(t, 4)
	for range 4 {
		if _, ok := h.register(); !ok {
			t.Fatal("register refused")
		}
	}
	f := frame(1, 10, 20)
	if a := testing.AllocsPerRun(1000, func() { h.Publish(f) }); a != 0 {
		t.Fatalf("Publish allocs/op with 4 clients = %v, want 0", a)
	}
	if s := h.Stats(); s.Clients != 4 || s.Dropped == 0 {
		t.Fatalf("stats %+v: want 4 clients and drop-oldest evictions", s)
	}
}

func TestHelloFirstAndFirstFrameStatus(t *testing.T) {
	h, _ := newFakeHub(t, 8)
	a := pipeConn(t, h)
	wantHello(t, a.next(t), StateWaiting)
	h.Publish(frame(1, 10, 20))
	wantStatus(t, a.next(t), StateLive, ReasonFirstFrame)
	wantSample(t, a.next(t), 1, 0)

	b := pipeConn(t, h) // hello says live: the first sample needs no status
	wantHello(t, b.next(t), StateLive)
	h.Publish(frame(2, 10, 20))
	wantSample(t, a.next(t), 2, 0)
	wantSample(t, b.next(t), 2, 0)
}

func TestInvalidInputNulled(t *testing.T) {
	h, _ := newFakeHub(t, 8)
	a := pipeConn(t, h)
	a.next(t)
	h.Publish(frame(1, float32(math.NaN()), 50))
	a.next(t) // first_frame
	m := a.next(t)
	wantSample(t, m, 1, 0)
	if m.Inputs.Brake != nil {
		t.Fatalf("NaN brake encoded as %v, want null", *m.Inputs.Brake)
	}
	waitFor(t, "3 sent", func() bool { return h.Stats().Sent == 3 }) // counted after send returns
	if s := h.Stats(); s.InvalidInputs != 1 {
		t.Fatalf("stats %+v: want 1 invalid input", s)
	}
}

func TestProjectErrorSkipsFrame(t *testing.T) {
	h, _ := newFakeHub(t, 8)
	a := pipeConn(t, h)
	a.next(t)
	bad := frame(1, 10, 20)
	bad.RecvTimestamp = nil
	h.Publish(bad)
	h.Publish(frame(2, 10, 20))
	wantStatus(t, a.next(t), StateLive, ReasonFirstFrame)
	wantSample(t, a.next(t), 2, 0) // first delivered sample on this connection
	if s := h.Stats(); s.ProjectErrors != 1 {
		t.Fatalf("stats %+v: want 1 project error", s)
	}
}

// TestStaleTiming uses the real monitor: stale arrives within [timeout, ~2x timeout] of the last
// frame, and resume emits source_live/source_resumed immediately before the next sample.
func TestStaleTiming(t *testing.T) {
	const timeout = 40 * time.Millisecond
	h := newRealHub(t, 8, timeout)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v", err)
		}
	}()

	conns := []*conn{pipeConn(t, h), pipeConn(t, h)}
	for _, cn := range conns {
		wantHello(t, cn.next(t), StateWaiting)
	}
	start := time.Now()
	h.Publish(frame(1, 10, 20))
	for _, cn := range conns {
		wantStatus(t, cn.next(t), StateLive, ReasonFirstFrame)
		wantSample(t, cn.next(t), 1, 0)
	}
	for _, cn := range conns {
		wantStatus(t, cn.next(t), StateStale, ReasonStaleTimeout)
	}
	if el := time.Since(start); el < timeout || el > 2*timeout+200*time.Millisecond {
		t.Fatalf("stale after %v, want within [%v, ~%v]", el, timeout, 2*timeout)
	}
	if st := h.Stats().State; st != StateStale {
		t.Fatalf("hub state %s", st)
	}
	h.Publish(frame(2, 10, 20))
	for _, cn := range conns {
		wantStatus(t, cn.next(t), StateLive, ReasonResumed)
		wantSample(t, cn.next(t), 2, 0)
	}
}

// scenario is a deterministic hub run that records every message one pipe client received.
type scenario func(t *testing.T) (lines [][]byte, h *Hub, cn *conn)

// record reads n messages and appends their raw bytes.
func record(t *testing.T, cn *conn, lines *[][]byte, n int) []msg {
	t.Helper()
	var ms []msg
	for range n {
		m := cn.next(t)
		*lines = append(*lines, m.raw)
		ms = append(ms, m)
	}
	return ms
}

var scenarios = map[string]scenario{
	// The source is already live at connect: hello(live) then samples with no status.
	"hello-then-samples": func(t *testing.T) ([][]byte, *Hub, *conn) {
		h, _ := newFakeHub(t, 8)
		h.Publish(frame(1, 10, 20)) // before any client: not offered to anyone
		cn := pipeConn(t, h)
		var lines [][]byte
		record(t, cn, &lines, 1)
		for s := uint64(2); s <= 4; s++ {
			h.Publish(frame(s, float32(s)*10, 100))
		}
		record(t, cn, &lines, 3)
		return lines, h, cn
	},
	"waiting-first-frame": func(t *testing.T) ([][]byte, *Hub, *conn) {
		h, _ := newFakeHub(t, 8)
		cn := pipeConn(t, h)
		var lines [][]byte
		record(t, cn, &lines, 1)
		h.Publish(frame(1, 0, 100))
		h.Publish(frame(2, 50, 0))
		record(t, cn, &lines, 3)
		return lines, h, cn
	},
	// Buffer 4; the writer is paused in send of sample 2 while 12 more frames (3..14) arrive.
	"drop-arithmetic": func(t *testing.T) ([][]byte, *Hub, *conn) {
		h, _ := newFakeHub(t, 4)
		cn := pipeConn(t, h)
		var lines [][]byte
		record(t, cn, &lines, 1)
		h.Publish(frame(1, 10, 20))
		record(t, cn, &lines, 2)
		h.Publish(frame(2, 10, 20))
		waitDequeued(t, cn)
		for s := uint64(3); s <= 14; s++ {
			h.Publish(frame(s, 10, 20))
		}
		record(t, cn, &lines, 5)
		return lines, h, cn
	},
	// Buffer 4; the writer is paused in send of sample 2 with a full backlog (5..8, 3..4 evicted)
	// when the source goes stale. stale goes ahead of the backlog, the backlog is discarded, and
	// the resumed sample's droppedSincePrevious covers evictions and discards.
	"stale-resume": func(t *testing.T) ([][]byte, *Hub, *conn) {
		h, clk := newFakeHub(t, 4)
		cn := pipeConn(t, h)
		var lines [][]byte
		record(t, cn, &lines, 1)
		h.Publish(frame(1, 10, 20))
		record(t, cn, &lines, 2)
		h.Publish(frame(2, 10, 20))
		waitDequeued(t, cn)
		for s := uint64(3); s <= 8; s++ {
			h.Publish(frame(s, 10, 20))
		}
		goStale(h, clk)
		record(t, cn, &lines, 2) // in-flight sample 2, then stale
		waitFor(t, "backlog discard", func() bool { return h.Stats().StaleDiscarded == 4 })
		h.Publish(frame(9, 10, 20))
		record(t, cn, &lines, 2)
		return lines, h, cn
	},
}

func TestTranscripts(t *testing.T) {
	sch := compileSchema(t)
	for name, run := range scenarios {
		t.Run(name, func(t *testing.T) {
			lines, _, _ := run(t)
			for i, l := range lines {
				if err := validateBytes(t, sch, l); err != nil {
					t.Errorf("line %d fails schema: %v\n%s", i+1, err, l)
				}
			}
			checkOrder(t, lines)
			got := append(bytes.Join(lines, []byte("\n")), '\n')
			path := filepath.Join("testdata", "v1", "transcripts", name+".jsonl")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("transcript differs from %s:\ngot:\n%s\nwant:\n%s", path, got, want)
			}
		})
	}
}

func TestDropArithmetic(t *testing.T) {
	lines, h, cn := scenarios["drop-arithmetic"](t)
	ms := make([]msg, len(lines))
	for i, l := range lines {
		ms[i] = decode(t, l)
	}
	wantHello(t, ms[0], StateWaiting)
	wantStatus(t, ms[1], StateLive, ReasonFirstFrame)
	wantSample(t, ms[2], 1, 0)
	wantSample(t, ms[3], 2, 0)
	wantSample(t, ms[4], 11, 8) // 3..10 evicted from the 4-slot queue
	for i, s := range []uint64{12, 13, 14} {
		wantSample(t, ms[5+i], s, 0)
	}
	if d := cn.c.dropped.Load(); d != 8 {
		t.Fatalf("client dropped %d, want 8", d)
	}
	if s := h.Stats(); s.Dropped != 8 || s.StaleDiscarded != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestStaleWithFullBacklog(t *testing.T) {
	lines, h, cn := scenarios["stale-resume"](t)
	ms := make([]msg, len(lines))
	for i, l := range lines {
		ms[i] = decode(t, l)
	}
	wantHello(t, ms[0], StateWaiting)
	wantStatus(t, ms[1], StateLive, ReasonFirstFrame)
	wantSample(t, ms[2], 1, 0)
	wantSample(t, ms[3], 2, 0) // was already in send when stale fired
	wantStatus(t, ms[4], StateStale, ReasonStaleTimeout)
	wantStatus(t, ms[5], StateLive, ReasonResumed)
	wantSample(t, ms[6], 9, 6) // 3,4 evicted + 5..8 discarded as stale
	if d := cn.c.dropped.Load(); d != 2 {
		t.Fatalf("client queue drops %d, want 2", d)
	}
	if s := h.Stats(); s.Dropped != 2 || s.StaleDiscarded != 4 || s.State != StateLive {
		t.Fatalf("stats %+v", s)
	}
}

// TestStaleRaceReverted: a frame published after the monitor read lastFrame but before its CAS
// must not leave the hub stale (the monitor's post-CAS re-check reverts it, without notifying).
func TestStaleRaceReverted(t *testing.T) {
	h, clk := newFakeHub(t, 4)
	c, _ := h.register()
	h.Publish(frame(1, 10, 20))
	clk.Add(int64(fakeTimeout))
	h.beforeStaleCAS = func() { h.Publish(frame(2, 10, 20)) }
	h.checkStale()
	if st := h.Stats().State; st != StateLive {
		t.Fatalf("state %s after racing frame, want live", st)
	}
	select {
	case <-c.notify:
		t.Fatal("clients notified of a reverted stale transition")
	default:
	}
}

// checkOrder asserts the ADR 0002 ordering invariants on one connection's transcript.
func checkOrder(t *testing.T, lines [][]byte) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("empty transcript")
	}
	first := decode(t, lines[0])
	if first.Type != "hello" {
		t.Fatalf("first message is %s", first.raw)
	}
	last := first.Status
	var prev uint64
	have := false
	for _, l := range lines[1:] {
		m := decode(t, l)
		switch m.Type {
		case "status":
			switch m.State {
			case StateStale:
				if last != StateLive {
					t.Fatalf("source_stale after %s", last)
				}
			case StateLive:
				want := ReasonResumed
				if last == StateWaiting {
					want = ReasonFirstFrame
				}
				if last == StateLive || m.Reason != want {
					t.Fatalf("%s after %s", m.raw, last)
				}
			}
			last = m.State
		case "sample":
			if last != StateLive {
				t.Fatalf("sample while last state sent is %s: %s", last, m.raw)
			}
			seq := m.seq(t)
			if have && seq <= prev {
				t.Fatalf("sequence %d after %d", seq, prev)
			}
			if m.Dropped != DroppedSince(prev, seq, have) {
				t.Fatalf("droppedSincePrevious %d, want %d", m.Dropped, DroppedSince(prev, seq, have))
			}
			prev, have = seq, true
		default:
			t.Fatalf("unexpected message %s", m.raw)
		}
	}
}

// capture records every message a writer sends.
type capture struct {
	mu    sync.Mutex
	lines [][]byte
	delay time.Duration
}

func (c *capture) send(ctx context.Context, b []byte) error {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	c.lines = append(c.lines, b)
	c.mu.Unlock()
	return ctx.Err()
}

// TestConcurrentConnectOrderingStress is a randomized real-time variant of
// TestConcurrentConnectOrdering: clients connect while frames flow and the real monitor
// repeatedly declares stale. Only the ordering invariants are asserted; whether a stale/resume
// actually occurred depends on scheduling and is only logged.
func TestConcurrentConnectOrderingStress(t *testing.T) {
	h := newRealHub(t, 4, 15*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-runDone; !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v", err)
		}
	}()

	stop := make(chan struct{})
	pubDone := make(chan struct{})
	go func() { // the single producer
		defer close(pubDone)
		for seq := uint64(1); ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			h.Publish(frame(seq, 10, 20))
			if seq%100 == 0 {
				time.Sleep(40 * time.Millisecond) // > timeout: stale, then resume
			} else {
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()
	caps := make([]*capture, 48)
	var wg sync.WaitGroup
	for i := range caps {
		caps[i] = &capture{}
		if i%3 == 0 {
			caps[i].delay = time.Millisecond // slow enough to overflow its queue
		}
		wg.Go(func() {
			time.Sleep(time.Duration(rand.IntN(200)) * time.Millisecond)
			c, ok := h.register()
			if !ok {
				t.Error("register refused")
				return
			}
			sctx, scancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer scancel()
			_ = h.stream(sctx, c, caps[i].send, time.Second)
		})
	}
	wg.Wait()
	close(stop)
	<-pubDone
	var stale, resumed bool
	for _, c := range caps {
		checkOrder(t, c.lines)
		for _, l := range c.lines {
			m := decode(t, l)
			stale = stale || m.State == StateStale
			resumed = resumed || m.Reason == ReasonResumed
		}
	}
	t.Logf("observed stale=%v resumed=%v", stale, resumed)
	if n := h.Stats().Clients; n != 0 {
		t.Fatalf("%d clients still registered", n)
	}
}

// countingPub stands in for the RTPS publisher: it serializes (reads) every frame, like a
// transport, and counts them.
type countingPub struct{ n int }

func (p *countingPub) Publish(f *telemetryv1.TelemetryFrame) {
	if _, err := proto.Marshal(f); err == nil {
		p.n++
	}
}

// lines returns a copy of the captured messages.
func (c *capture) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.lines...)
}

// lastIs reports whether the newest captured message satisfies ok (false when nothing captured).
func (c *capture) lastIs(t *testing.T, ok func(msg) bool) bool {
	t.Helper()
	l := c.snapshot()
	return len(l) > 0 && ok(decode(t, l[len(l)-1]))
}

func isSample(t *testing.T, seq uint64) func(msg) bool {
	return func(m msg) bool { return m.Type == "sample" && m.seq(t) == seq }
}

// TestSlowClientIsolation: the slow client delivers hello, first_frame and sample 1, then blocks in
// send holding sample 2 while frames 3..64 arrive. The fast client and the RTPS fake get every
// frame; the slow client's 4-slot queue keeps 61..64 and evicts 3..60.
func TestSlowClientIsolation(t *testing.T) {
	const n = 64
	h, _ := newFakeHub(t, 4)
	fast := pipeConn(t, h)
	wantHello(t, fast.next(t), StateWaiting)

	gate := make(chan struct{})
	blocked := make(chan struct{})
	slowCap := &capture{}
	var calls atomic.Int64
	slow := dial(t, h, func(ctx context.Context, b []byte) error {
		if calls.Add(1) == 4 { // hello, first_frame and sample 1 pass; sample 2 blocks
			close(blocked)
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return slowCap.send(ctx, b)
	}, time.Minute)

	rtps := &countingPub{}
	dests := []ingest.Publisher{h, rtps} // same shape as the CLI fan-out
	publish := func(s uint64) {
		for _, d := range dests {
			d.Publish(frame(s, 10, 20))
		}
	}
	waitFor(t, "slow hello", func() bool { return slowCap.lastIs(t, func(m msg) bool { return m.Type == "hello" }) })
	publish(1)
	wantStatus(t, fast.next(t), StateLive, ReasonFirstFrame)
	wantSample(t, fast.next(t), 1, 0)
	waitFor(t, "slow client to deliver sample 1", func() bool { return slowCap.lastIs(t, isSample(t, 1)) })
	publish(2)
	wantSample(t, fast.next(t), 2, 0)
	select { // the slow writer holds sample 2 in send; its queue is empty
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("slow writer never blocked on sample 2")
	}
	for s := uint64(3); s <= n; s++ {
		publish(s)
		wantSample(t, fast.next(t), s, 0)
	}
	if rtps.n != n {
		t.Fatalf("RTPS fake got %d of %d frames", rtps.n, n)
	}
	close(gate)
	waitFor(t, "slow client to deliver the newest frame", func() bool { return slowCap.lastIs(t, isSample(t, n)) })

	lines := slowCap.snapshot()
	checkOrder(t, lines)
	wantHello(t, decode(t, lines[0]), StateWaiting)
	wantStatus(t, decode(t, lines[1]), StateLive, ReasonFirstFrame)
	// Hand-computed: 62 frames (3..64) into a 4-slot queue keep 61..64 and evict 58.
	want := []struct{ seq, dropped uint64 }{{1, 0}, {2, 0}, {61, 58}, {62, 0}, {63, 0}, {64, 0}}
	if len(lines) != 2+len(want) {
		t.Fatalf("slow client got %d messages, want %d", len(lines), 2+len(want))
	}
	for i, w := range want {
		wantSample(t, decode(t, lines[2+i]), w.seq, w.dropped)
	}
	if d := slow.c.dropped.Load(); d != 58 {
		t.Fatalf("slow client queue drops %d, want 58", d)
	}
	if fd := fast.c.dropped.Load(); fd != 0 {
		t.Fatalf("fast client dropped %d", fd)
	}
	if s := h.Stats(); s.Dropped != 58 {
		t.Fatalf("hub dropped %d, want sum of per-client drops 58", s.Dropped)
	}
}

func TestWriteTimeoutClosesSlowClient(t *testing.T) {
	h, _ := newFakeHub(t, 4)
	var calls atomic.Int64
	cn := dial(t, h, func(ctx context.Context, _ []byte) error {
		if calls.Add(1) == 1 {
			return nil // hello
		}
		<-ctx.Done() // a peer that never reads
		return ctx.Err()
	}, 20*time.Millisecond)
	h.Publish(frame(1, 10, 20))
	select {
	case <-cn.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not return after the write timeout")
	}
	if !errors.Is(cn.err, context.DeadlineExceeded) {
		t.Fatalf("stream error %v, want deadline exceeded", cn.err)
	}
	if s := h.Stats(); s.SlowClosed != 1 || s.Clients != 0 {
		t.Fatalf("stats %+v: want 1 slow close and 0 clients", s)
	}
}

func TestFrameImmutability(t *testing.T) {
	h, _ := newFakeHub(t, 8)
	a, b := pipeConn(t, h), pipeConn(t, h)
	a.next(t)
	b.next(t)
	f := frame(1, 10, 20)
	f.Gap = true
	f.CarSpeed = 42
	orig := proto.Clone(f)
	for _, d := range []ingest.Publisher{h, &countingPub{}} {
		d.Publish(f)
	}
	for _, cn := range []*conn{a, b} {
		cn.next(t)
		wantSample(t, cn.next(t), 1, 0)
	}
	if !proto.Equal(orig, f) {
		t.Fatal("frame mutated by a consumer")
	}
}

func TestConnectDisconnectNoLeak(t *testing.T) {
	h, _ := newFakeHub(t, 4)
	before := runtime.NumGoroutine()
	stop := make(chan struct{})
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		for seq := uint64(1); ; seq++ {
			select {
			case <-stop:
				return
			default:
				h.Publish(frame(seq, 10, 20))
			}
		}
	}()
	discard := func(context.Context, []byte) error { return nil }
	for i := range 1000 {
		c, ok := h.register()
		if !ok {
			t.Fatal("register refused")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.stream(ctx, c, discard, time.Second) }()
		if i%2 == 0 {
			cancel()
		} else {
			h.unregister(c)
		}
		if err := <-done; i%2 == 1 && err != nil {
			t.Fatalf("stream after unregister returned %v, want nil", err)
		}
		cancel()
	}
	close(stop)
	<-pubDone
	if s := h.Stats(); s.Clients != 0 || len(*h.clients.Load()) != 0 {
		t.Fatalf("clients left registered: %+v", s)
	}
	waitFor(t, "goroutines to settle", func() bool { return runtime.NumGoroutine() <= before+2 })
}

func TestRunAndStreamReturn(t *testing.T) {
	h := newRealHub(t, 4, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()

	c, _ := h.register()
	sctx, scancel := context.WithCancel(context.Background())
	sdone := make(chan error, 1)
	go func() { sdone <- h.stream(sctx, c, func(context.Context, []byte) error { return nil }, time.Second) }()
	scancel()
	if err := <-sdone; !errors.Is(err, context.Canceled) {
		t.Fatalf("stream on cancel returned %v", err)
	}

	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on cancel")
	}
	if _, ok := h.register(); ok {
		t.Fatal("register accepted after Run returned")
	}
}

// TestStalledPublishNoFalseResume: a Publish that stalls between its state check and its enqueue
// for longer than the timeout lets the monitor declare stale (staleAtSeq = that frame) and the
// writer finish its stale handling before the frame is queued. The late frame must still be
// discarded, not announced with source_resumed.
func TestStalledPublishNoFalseResume(t *testing.T) {
	h, clk := newFakeHub(t, 4)
	cn := pipeConn(t, h)
	wantHello(t, cn.next(t), StateWaiting)
	h.Publish(frame(1, 10, 20))
	wantStatus(t, cn.next(t), StateLive, ReasonFirstFrame)
	wantSample(t, cn.next(t), 1, 0)

	h.beforeEnqueue = func() {
		goStale(h, clk) // frame 2's lastFrame is already older than the timeout
		wantStatus(t, cn.next(t), StateStale, ReasonStaleTimeout)
		// Not a correctness wait: gives the writer time to finish stale handling so the
		// unfixed hub reliably exhibits the bug. The fixed hub passes either way.
		time.Sleep(20 * time.Millisecond)
	}
	h.Publish(frame(2, 10, 20))
	h.beforeEnqueue = nil

	h.Publish(frame(3, 10, 20)) // a genuinely fresh frame
	wantStatus(t, cn.next(t), StateLive, ReasonResumed)
	wantSample(t, cn.next(t), 3, 1) // hand-computed: frame 2 discarded, 3-1-1 = 1
	if s := h.Stats(); s.StaleDiscarded != 1 || s.Dropped != 0 {
		t.Fatalf("stats %+v: want 1 stale discard, 0 queue drops", s)
	}
}

func TestTinySourceTimeoutDoesNotPanic(t *testing.T) {
	h := newRealHub(t, 1, time.Nanosecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v", err)
	}
}

// TestWaitingBurstDiscardedThenRecovery: the client's hello says waiting and the writer is still
// blocked sending it while the source sends a burst and goes stale. The burst is discarded, no
// source_stale is sent (the client never saw live), and the next frame gets first_frame.
func TestWaitingBurstDiscardedThenRecovery(t *testing.T) {
	h, clk := newFakeHub(t, 4)
	cn := pipeConn(t, h)
	waitFor(t, "writer blocked in hello send", func() bool { return cn.sends.Load() == 1 })
	for s := uint64(1); s <= 3; s++ {
		h.Publish(frame(s, 10, 20))
	}
	goStale(h, clk)
	wantHello(t, cn.next(t), StateWaiting)
	waitFor(t, "burst discard", func() bool { return h.Stats().StaleDiscarded == 3 })
	h.Publish(frame(4, 10, 20))
	wantStatus(t, cn.next(t), StateLive, ReasonFirstFrame)
	wantSample(t, cn.next(t), 4, 0) // first sample on the connection: 0 by rule
	if s := h.Stats(); s.Dropped != 0 || s.State != StateLive {
		t.Fatalf("stats %+v", s)
	}
}

// TestConcurrentConnectOrdering drives stale/resume deterministically (fake clock, monitor steps
// on the producer goroutine) while other clients connect concurrently at every round.
func TestConcurrentConnectOrdering(t *testing.T) {
	const rounds, perRound, pre, joiners = 5, 10, 8, 24
	h, clk := newFakeHub(t, 4)
	preCaps := make([]*capture, pre)
	for i := range preCaps {
		preCaps[i] = &capture{}
		dial(t, h, preCaps[i].send, time.Minute)
	}
	for _, c := range preCaps {
		waitFor(t, "hello", func() bool { return c.lastIs(t, func(m msg) bool { return m.Type == "hello" }) })
	}

	jctx, jcancel := context.WithCancel(context.Background())
	defer jcancel()
	start := make([]chan struct{}, rounds)
	for r := range start {
		start[r] = make(chan struct{})
	}
	joinCaps := make([]*capture, joiners)
	var wg sync.WaitGroup
	for i := range joinCaps {
		joinCaps[i] = &capture{}
		wg.Go(func() {
			<-start[i%rounds]
			c, ok := h.register()
			if !ok {
				t.Error("register refused")
				return
			}
			_ = h.stream(jctx, c, joinCaps[i].send, time.Minute)
		})
	}

	seq := uint64(0)
	for r := range rounds {
		close(start[r]) // joiners race this round's publishes and stale transition
		for range perRound {
			seq++
			h.Publish(frame(seq, 10, 20))
		}
		for _, c := range preCaps {
			waitFor(t, "pre-registered client to catch up", func() bool { return c.lastIs(t, isSample(t, seq)) })
		}
		goStale(h, clk)
		for _, c := range preCaps {
			waitFor(t, "source_stale", func() bool { return c.lastIs(t, func(m msg) bool { return m.State == StateStale }) })
		}
	}
	seq++
	h.Publish(frame(seq, 10, 20))
	for _, c := range preCaps {
		waitFor(t, "final sample", func() bool { return c.lastIs(t, isSample(t, seq)) })
	}
	jcancel()
	wg.Wait()

	for _, c := range preCaps {
		lines := c.snapshot()
		checkOrder(t, lines)
		var stale, resumed int
		for _, l := range lines {
			m := decode(t, l)
			if m.State == StateStale {
				stale++
			}
			if m.Reason == ReasonResumed {
				resumed++
			}
		}
		if stale != rounds || resumed != rounds {
			t.Fatalf("pre-registered client saw %d stale / %d resumed, want %d each", stale, resumed, rounds)
		}
	}
	for _, c := range joinCaps {
		checkOrder(t, c.snapshot())
	}
}
