package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
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
