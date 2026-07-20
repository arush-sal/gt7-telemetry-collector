package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeServer records the frames it receives and signals each on recv.
type fakeServer struct {
	telemetryv1.UnimplementedTelemetryServer
	mu   sync.Mutex
	got  []*telemetryv1.TelemetryFrame
	recv chan struct{}
}

func (s *fakeServer) Ingest(stream telemetryv1.Telemetry_IngestServer) error {
	for {
		f, err := stream.Recv()
		if err == io.EOF {
			s.mu.Lock()
			n := len(s.got)
			s.mu.Unlock()
			return stream.SendAndClose(&telemetryv1.Ack{FramesReceived: uint64(n)})
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.got = append(s.got, f)
		s.mu.Unlock()
		s.recv <- struct{}{}
	}
}

// TestPublisherStreamsToServer runs the publisher against an in-process bufconn gRPC server and
// asserts the frames it publishes arrive in order.
func TestPublisherStreamsToServer(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeServer{recv: make(chan struct{}, 8)}
	telemetryv1.RegisterTelemetryServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	dialer := grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(context.Background())
	})
	p := NewPublisher("passthrough:///bufnet", 8, dialer, grpc.WithTransportCredentials(insecure.NewCredentials()))

	go func() { _ = p.Run(t.Context()) }()

	const n = 5
	for i := 1; i <= n; i++ {
		p.Publish(&telemetryv1.TelemetryFrame{Sequence: uint64(i)})
	}

	// Wait for the server to receive all n frames (or fail on timeout).
	deadline := time.After(3 * time.Second)
	for i := range n {
		select {
		case <-fake.recv:
		case <-deadline:
			t.Fatalf("timed out after receiving %d/%d frames", i, n)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.got) != n {
		t.Fatalf("received %d frames, want %d", len(fake.got), n)
	}
	for i, f := range fake.got {
		if f.Sequence != uint64(i+1) {
			t.Errorf("frame %d: sequence = %d, want %d", i, f.Sequence, i+1)
		}
	}
}

// TestPublisherWarnsOncePerOutage asserts the reconnect loop logs a single Warn per outage (not one
// per retry attempt), stays quiet while the RTPS is down, and logs one Info when it recovers. A
// second outage warns again. It drives the loop with a scripted attempt function so the log edges
// are deterministic and independent of real networking/timing.
func TestPublisherWarnsOncePerOutage(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	p := NewPublisher("rtps.test:50051", 8)
	p.SetLogger(zap.New(core))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fail := errors.New("connection refused")
	// Two outages separated by a recovery: [down x3] -> [up] -> [down x1].
	script := []struct {
		sent bool
		err  error
	}{
		{false, fail}, {false, fail}, {false, fail}, // outage 1: 3 failed attempts -> expect 1 Warn
		{true, nil},   // recovery -> expect 1 Info
		{false, fail}, // outage 2 -> expect 1 more Warn
	}
	i := 0
	attempt := func(context.Context) (bool, error) {
		if i >= len(script) {
			cancel() // script exhausted; unwind the loop
			return false, context.Canceled
		}
		o := script[i]
		i++
		return o.sent, o.err
	}

	if err := p.run(ctx, attempt, time.Millisecond, 2*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want context.Canceled", err)
	}

	if warn := logs.FilterLevelExact(zapcore.WarnLevel).Len(); warn != 2 {
		t.Errorf("Warn logs = %d, want 2 (one per outage, not one per retry)", warn)
	}
	if info := logs.FilterLevelExact(zapcore.InfoLevel).Len(); info != 1 {
		t.Errorf("Info logs = %d, want 1 (one recovery)", info)
	}
}

// TestPublisherFlappingDoesNotSpam covers the case the real streamOnce actually returns on a
// mid-stream drop: sent==true (it reconnected and forwarded frames) together with a non-nil error
// (the stream then broke). A connection that repeatedly connects-sends-drops must NOT emit a Warn
// (and Info) on every cycle — that would re-introduce the very spam this change removes. Expect a
// single Warn for the initial outage and a single Info when it first comes back, then silence.
func TestPublisherFlappingDoesNotSpam(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	p := NewPublisher("rtps.test:50051", 8)
	p.SetLogger(zap.New(core))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fail := errors.New("stream broke")
	// One real outage, then a flapping connection (connect+send+drop each cycle).
	script := []struct {
		sent bool
		err  error
	}{
		{false, fail}, // outage -> 1 Warn
		{true, fail},  // reconnected+sent then dropped -> 1 Info, no re-Warn
		{true, fail},  // still flapping -> silent
		{true, fail},  // still flapping -> silent
	}
	i := 0
	attempt := func(context.Context) (bool, error) {
		if i >= len(script) {
			cancel()
			return false, context.Canceled
		}
		o := script[i]
		i++
		return o.sent, o.err
	}

	if err := p.run(ctx, attempt, time.Millisecond, 2*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want context.Canceled", err)
	}

	if warn := logs.FilterLevelExact(zapcore.WarnLevel).Len(); warn != 1 {
		t.Errorf("Warn logs = %d, want 1 (flapping must not spam)", warn)
	}
	if info := logs.FilterLevelExact(zapcore.InfoLevel).Len(); info != 1 {
		t.Errorf("Info logs = %d, want 1 (single recovery)", info)
	}
}

// TestPublishDropsOldest checks the drop-oldest policy without any consumer: with a full buffer the
// oldest frame is evicted and counted, and the freshest frames remain.
func TestPublishDropsOldest(t *testing.T) {
	p := NewPublisher("unused", 2) // no Run goroutine draining
	for i := 1; i <= 3; i++ {
		p.Publish(&telemetryv1.TelemetryFrame{Sequence: uint64(i)})
	}
	if p.Dropped() != 1 {
		t.Errorf("Dropped = %d, want 1", p.Dropped())
	}
	// Buffer (cap 2) should now hold the two freshest: sequences 2 and 3.
	first := <-p.ch
	second := <-p.ch
	if first.Sequence != 2 || second.Sequence != 3 {
		t.Errorf("remaining sequences = %d,%d, want 2,3", first.Sequence, second.Sequence)
	}
}
