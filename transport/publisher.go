package transport

import (
	"context"
	"sync/atomic"
	"time"

	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 30 * time.Second
)

// Publisher streams canonical telemetry frames to the RTPS over a client-streaming gRPC RPC. It
// decouples the receive path from the network: Publish enqueues without blocking (dropping the
// oldest frame when the buffer is full), while Run owns dialing, streaming, and reconnect/backoff.
//
// Publish is safe to call from exactly one producer goroutine (the receive path); it is not safe
// for concurrent Publish callers.
type Publisher struct {
	addr     string
	dialOpts []grpc.DialOption
	ch       chan *telemetryv1.TelemetryFrame
	dropped  atomic.Uint64
	log      *zap.Logger
}

// NewPublisher returns a Publisher targeting addr with a bufSize-deep outbound buffer. dialOpts are
// passed to grpc.NewClient and must include transport credentials (e.g. insecure.NewCredentials()).
func NewPublisher(addr string, bufSize int, dialOpts ...grpc.DialOption) *Publisher {
	return &Publisher{
		addr:     addr,
		dialOpts: dialOpts,
		ch:       make(chan *telemetryv1.TelemetryFrame, bufSize),
		log:      zap.NewNop(),
	}
}

// SetLogger attaches a logger used to report reconnect attempts. Optional; the zero logger is a
// no-op. Call before Run.
func (p *Publisher) SetLogger(log *zap.Logger) { p.log = log }

// Publish enqueues a frame for delivery without blocking. When the buffer is full it evicts the
// oldest frame (keeping the freshest telemetry) and increments the dropped counter. Single producer
// only.
func (p *Publisher) Publish(f *telemetryv1.TelemetryFrame) {
	for {
		select {
		case p.ch <- f:
			return
		default:
			select {
			case <-p.ch:
				p.dropped.Add(1)
			default:
			}
		}
	}
}

// Dropped returns the number of frames discarded because the outbound buffer was full.
func (p *Publisher) Dropped() uint64 { return p.dropped.Load() }

// Run dials the RTPS and streams buffered frames until ctx is cancelled, reconnecting with capped
// exponential backoff on any stream error. It returns ctx.Err() on shutdown.
func (p *Publisher) Run(ctx context.Context) error {
	return p.run(ctx, p.streamOnce, initialBackoff, maxBackoff)
}

// run is the reconnect loop, parameterized on the per-attempt function and the backoff bounds so it
// can be driven deterministically in tests. attempt reports whether it made progress (sent) and any
// stream error.
func (p *Publisher) run(ctx context.Context, attempt func(context.Context) (bool, error), initial, max time.Duration) error {
	backoff := initial
	// warned is edge-triggered logging state: true once the current outage has been reported, so
	// repeated failed reconnect attempts stay quiet (Debug) instead of spamming Warn. Any attempt
	// that made progress (sent a frame) clears it, so a later outage warns again. The outage Warn is
	// emitted ONLY on a no-progress attempt: streamOnce returns sent=true together with the error of
	// a *subsequent* stream break, and treating that as an outage would re-warn (and re-Info) on
	// every flap. A genuine outage produces sent=false attempts, which is where we warn.
	warned := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sent, err := attempt(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sent {
			// Progress was made: the connection is (or just was) up. Reset backoff, and announce
			// recovery once if we had previously reported an outage.
			backoff = initial
			if warned {
				p.log.Info("RTPS connection re-established", zap.String("addr", p.addr))
				warned = false
			}
		} else {
			backoff = min(2*backoff, max)
			if err != nil && !warned {
				p.log.Warn("RTPS unavailable; buffering frames and retrying silently until it returns",
					zap.String("addr", p.addr), zap.Error(err))
				warned = true
			} else if err != nil {
				// Already reported this outage; keep retrying silently (detail at Debug only).
				p.log.Debug("publisher reconnect attempt failed",
					zap.String("addr", p.addr), zap.Duration("backoff", backoff), zap.Error(err))
			}
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// streamOnce opens one Ingest stream and forwards frames until ctx is cancelled or a send fails. It
// reports whether at least one frame was sent (used to reset backoff).
func (p *Publisher) streamOnce(ctx context.Context) (sent bool, err error) {
	conn, err := grpc.NewClient(p.addr, p.dialOpts...)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()

	stream, err := telemetryv1.NewTelemetryClient(conn).Ingest(ctx)
	if err != nil {
		return false, err
	}
	p.log.Debug("telemetry stream opened", zap.String("addr", p.addr))

	for {
		select {
		case <-ctx.Done():
			// Best-effort close to receive the final Ack; the error is irrelevant on shutdown.
			_, _ = stream.CloseAndRecv()
			return sent, ctx.Err()
		case f := <-p.ch:
			if err := stream.Send(f); err != nil {
				return sent, err
			}
			sent = true
		}
	}
}
