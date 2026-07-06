package ingest

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arush-sal/garage/backend/collector/protocol"
	"go.uber.org/zap"
)

// readBufSize is the receive buffer; comfortably larger than a GT7 packet (~296 B).
const readBufSize = 4096

// Receiver owns the UDP socket. It runs the receive loop, copies each datagram off the reused read
// buffer, and hands it to the injected handler; concurrently it sends the "A" heartbeat that keeps
// GT7 streaming. The heartbeat and receive share one socket, which net.UDPConn allows.
type Receiver struct {
	conn     *net.UDPConn
	sendAddr *net.UDPAddr
	// hbInterval holds the heartbeat interval as int64 nanoseconds so it can be read by the
	// heartbeat goroutine and written by SetHeartbeatInterval (config hot-reload) without a race.
	hbInterval atomic.Int64
	// hbReconfigure wakes the heartbeat loop immediately when the interval changes, instead of
	// waiting for the in-flight timer (armed with the old interval) to fire. Buffered 1 and
	// non-blocking on send: at most one pending reconfigure matters, the interval value itself
	// always comes from hbInterval.
	hbReconfigure chan struct{}
	handle        func(payload []byte, recvTS time.Time)
	log           *zap.Logger
}

// NewReceiver binds the UDP receive port and resolves the heartbeat destination. It returns an
// error on an invalid console IP or if the port cannot be bound. Run must be called to start (and
// eventually close) the socket.
func NewReceiver(recvPort, sendPort int, psIP string, hbInterval time.Duration, handle func([]byte, time.Time)) (*Receiver, error) {
	ip := net.ParseIP(psIP)
	if ip == nil {
		return nil, fmt.Errorf("receiver: invalid console IP %q", psIP)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: recvPort})
	if err != nil {
		return nil, fmt.Errorf("receiver: bind :%d: %w", recvPort, err)
	}
	r := &Receiver{
		conn:          conn,
		sendAddr:      &net.UDPAddr{IP: ip, Port: sendPort},
		hbReconfigure: make(chan struct{}, 1),
		handle:        handle,
		log:           zap.NewNop(),
	}
	r.hbInterval.Store(int64(hbInterval))
	return r, nil
}

// SetHeartbeatInterval changes the heartbeat send interval while Run is active (config hot-reload).
// It is concurrency-safe (atomic-backed) and wakes the heartbeat loop so the new interval takes
// effect immediately rather than after the currently-armed timer fires.
func (r *Receiver) SetHeartbeatInterval(d time.Duration) {
	r.hbInterval.Store(int64(d))
	select {
	case r.hbReconfigure <- struct{}{}:
	default: // a reconfigure is already pending; the loop will pick up the latest value anyway
	}
}

// SetLogger attaches a logger for lifecycle and error events (not the per-packet path). Optional;
// the zero logger is a no-op. Call before Run.
func (r *Receiver) SetLogger(log *zap.Logger) { r.log = log }

// LocalAddr reports the bound receive address (useful once recvPort was 0 for an ephemeral port).
func (r *Receiver) LocalAddr() net.Addr { return r.conn.LocalAddr() }

// Run drives the receive loop until ctx is cancelled (or an unexpected read error occurs), then
// shuts down cleanly. Shutdown order: cancel -> heartbeat stops and the closer closes the socket,
// which unblocks the in-flight ReadFromUDP so the loop can exit. Run waits for all goroutines
// before returning.
func (r *Receiver) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r.log.Debug("receiver listening",
		zap.Stringer("addr", r.conn.LocalAddr()),
		zap.Stringer("heartbeat_to", r.sendAddr),
		zap.Duration("heartbeat_interval", time.Duration(r.hbInterval.Load())))

	var wg sync.WaitGroup

	// Heartbeat: send once now, then on the interval, until ctx is cancelled.
	wg.Go(func() { r.heartbeat(ctx) })

	// Closer: closing the socket is what unblocks a ReadFromUDP that is parked waiting for a packet
	// (context cancellation alone does not interrupt it).
	wg.Go(func() {
		<-ctx.Done()
		_ = r.conn.Close()
	})

	buf := make([]byte, readBufSize)
	var loopErr error
	for {
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				loopErr = err // unexpected error, not our shutdown
			}
			break
		}
		// R2: the read buffer is reused every iteration and the payload is handed to async stages
		// (raw dump, publish), so it must be copied before handoff.
		cp := make([]byte, n)
		copy(cp, buf[:n])
		r.handle(cp, time.Now())
	}

	cancel()  // ensure heartbeat + closer wind down even on an unexpected read error
	wg.Wait() // no leaked goroutines
	if loopErr != nil {
		r.log.Error("receiver read failed", zap.Error(loopErr))
		return loopErr
	}
	return ctx.Err()
}

// heartbeat sends the GT7 keep-alive byte immediately and then every hbInterval until ctx ends. A
// time.Timer (not Ticker) is used and re-armed off the atomic interval each time, so a reconfigure
// via SetHeartbeatInterval takes effect on the very next wakeup rather than only after Run was
// restarted with a new fixed value. Write errors (e.g. a stray send racing socket close during
// shutdown) are harmless and ignored.
func (r *Receiver) heartbeat(ctx context.Context) {
	_, _ = r.conn.WriteToUDP(protocol.HeartbeatPayload(), r.sendAddr)
	t := time.NewTimer(time.Duration(r.hbInterval.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.hbReconfigure:
			// Interval changed: rearm immediately with the new value instead of waiting for the
			// timer armed with the stale one. Pre-Go 1.23, Stop returning false meant a fire had
			// already been queued on t.C and had to be drained before Reset, or the loop could
			// select a stale tick. As of Go 1.23 the runtime changed Timer semantics so a fired
			// timer's channel send and Stop/Reset are mutually exclusive: once Stop (or Reset)
			// returns, t.C will not receive that old firing, so the drain is unnecessary and Stop's
			// boolean return no longer needs checking here.
			t.Stop()
			t.Reset(time.Duration(r.hbInterval.Load()))
		case <-t.C:
			_, _ = r.conn.WriteToUDP(protocol.HeartbeatPayload(), r.sendAddr)
			t.Reset(time.Duration(r.hbInterval.Load()))
		}
	}
}
