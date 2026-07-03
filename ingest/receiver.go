package ingest

import (
	"context"
	"fmt"
	"net"
	"sync"
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
	conn       *net.UDPConn
	sendAddr   *net.UDPAddr
	hbInterval time.Duration
	handle     func(payload []byte, recvTS time.Time)
	log        *zap.Logger
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
	return &Receiver{
		conn:       conn,
		sendAddr:   &net.UDPAddr{IP: ip, Port: sendPort},
		hbInterval: hbInterval,
		handle:     handle,
		log:        zap.NewNop(),
	}, nil
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
		zap.Duration("heartbeat_interval", r.hbInterval))

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

// heartbeat sends the GT7 keep-alive byte immediately and then every hbInterval until ctx ends.
// Write errors (e.g. a stray send racing socket close during shutdown) are harmless and ignored.
func (r *Receiver) heartbeat(ctx context.Context) {
	_, _ = r.conn.WriteToUDP(protocol.HeartbeatPayload(), r.sendAddr)
	t := time.NewTicker(r.hbInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = r.conn.WriteToUDP(protocol.HeartbeatPayload(), r.sendAddr)
		}
	}
}
