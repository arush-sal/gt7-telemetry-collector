package transport

import (
	"testing"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"google.golang.org/grpc"
)

// BenchmarkPublisherPublishDropOldest measures Publish when the buffer is permanently full, so
// every call takes the evict-oldest path. No Run goroutine and no network.
func BenchmarkPublisherPublishDropOldest(b *testing.B) {
	p := NewPublisher("unused", 4, grpc.EmptyDialOption{})
	f := &telemetryv1.TelemetryFrame{}
	for range 4 {
		p.Publish(f)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Publish(f)
	}
	b.StopTimer()
	if p.Dropped() != uint64(b.N) {
		b.Fatalf("dropped %d, want %d", p.Dropped(), b.N)
	}
}
