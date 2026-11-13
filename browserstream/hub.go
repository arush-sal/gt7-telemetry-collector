package browserstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
)

// HubConfig configures a Hub.
type HubConfig struct {
	SessionID        string        // required, from NewSessionID
	CollectorVersion string        // reported in hello
	ClientBuffer     int           // per-client sample queue capacity (>=1)
	SourceTimeout    time.Duration // no-frame interval before source_stale (>0)
}

// HubStats is a snapshot of the hub's counters (ADR 0003 metrics).
type HubStats struct {
	Clients        int    // currently registered clients
	Accepted       uint64 // clients ever registered (connections accepted by the hub)
	Sent           uint64 // messages written successfully (hello, status and sample)
	Dropped        uint64 // samples evicted from client queues (sum of per-client drops)
	InvalidInputs  uint64 // brake/throttle values nulled by Project
	ProjectErrors  uint64 // frames skipped because Project failed
	SlowClosed     uint64 // writers closed because a write exceeded its deadline
	StaleDiscarded uint64 // queued samples discarded on source_stale (seq <= staleAtSequence)
	State          SourceState
}

// Source states packed into the low bits of Hub.state; the generation counter fills the rest, so
// every transition changes the word and a CAS cannot succeed against an outdated transition.
const (
	stWaiting uint64 = iota
	stLive
	stStale
	stMask    = 3
	genShift  = 2
	genOneInc = 1 << genShift
)

var stateNames = [...]SourceState{stWaiting: StateWaiting, stLive: StateLive, stStale: StateStale}

// Hub fans canonical frames out to browser clients. It implements ingest.Publisher.
//
// Publish is the single producer (the receive goroutine). It never blocks and never allocates: it
// stores the last-frame time and sequence, flips waiting/stale to live with one CAS, and does a
// drop-oldest enqueue of the frame pointer into each client's bounded queue. Run owns the
// live -> stale transition. Each client has one writer (stream) that projects, marshals and
// writes; a slow client only overflows its own queue. See ADR 0001 and ADR 0002.
type Hub struct {
	sessionID, version string
	buf                int
	timeout            time.Duration
	now                func() time.Duration // monotonic time since NewHub; replaced in tests

	// beforeStaleCAS, when set (tests only), runs between the monitor's staleness decision and its
	// CAS, to inject a racing Publish.
	beforeStaleCAS func()
	// beforeEnqueue, when set (tests only), runs in Publish between the state flip and the enqueue,
	// to model a Publish that stalls past the source timeout.
	beforeEnqueue func()

	clients atomic.Pointer[[]*client] // copy-on-write snapshot read by Publish
	mu      sync.Mutex                // guards clients rebuilds and closed; never taken by Publish
	closed  bool                      // set when Run returns; register then refuses

	// Publish writes lastFrame, then lastSeq, then frames; checkStale reads them in reverse, so a
	// lastSeq it reads always belongs to a frame no newer than the lastFrame it reads.
	lastFrame  atomic.Int64  // h.now() nanos of the last Publish
	lastSeq    atomic.Uint64 // Sequence of the last Publish
	frames     atomic.Uint64 // Publish calls; change detector for checkStale
	state      atomic.Uint64 // st* | generation<<genShift
	staleAtSeq atomic.Uint64 // lastSeq when the source last went stale

	accepted, sent, dropped, invalid, projErr, slowClosed, staleDiscarded atomic.Uint64
}

// client is one registered connection. samples is never closed (Publish may still hold a stale
// snapshot that includes this client); done signals unregistration.
type client struct {
	samples   chan *telemetryv1.TelemetryFrame
	notify    chan struct{} // cap 1, coalescing: a stale transition happened
	done      chan struct{}
	closeOnce sync.Once
	dropped   atomic.Uint64
}

// NewHub validates cfg and returns a hub in state waiting_for_source.
func NewHub(cfg HubConfig) (*Hub, error) {
	switch {
	case cfg.SessionID == "":
		return nil, errors.New("browserstream: SessionID is required")
	case cfg.ClientBuffer < 1:
		return nil, errors.New("browserstream: ClientBuffer must be >= 1")
	case cfg.SourceTimeout <= 0:
		return nil, errors.New("browserstream: SourceTimeout must be > 0")
	}
	epoch := time.Now()
	h := &Hub{
		sessionID: cfg.SessionID,
		version:   cfg.CollectorVersion,
		buf:       cfg.ClientBuffer,
		timeout:   cfg.SourceTimeout,
		now:       func() time.Duration { return time.Since(epoch) },
	}
	h.clients.Store(&[]*client{})
	return h, nil
}

// Publish offers f to every registered client without blocking, evicting the client's oldest
// queued frame when its queue is full. Single producer only; f must not be mutated afterwards.
//
// Drop-oldest is best-effort: the client's writer drains the same queue concurrently, so an
// eviction may remove a frame the writer was about to take, and a frame may occasionally be
// evicted when one receive would have sufficed. Counters count actual evictions.
func (h *Hub) Publish(f *telemetryv1.TelemetryFrame) {
	h.lastFrame.Store(int64(h.now()))
	h.lastSeq.Store(f.Sequence)
	h.frames.Add(1)
	for {
		s := h.state.Load()
		if s&stMask == stLive || h.state.CompareAndSwap(s, ((s&^stMask)+genOneInc)|stLive) {
			break
		}
	}
	if h.beforeEnqueue != nil {
		h.beforeEnqueue()
	}
	for _, c := range *h.clients.Load() {
		for {
			select {
			case c.samples <- f:
			default:
				select {
				case <-c.samples:
					c.dropped.Add(1)
					h.dropped.Add(1)
				default:
				}
				continue
			}
			break
		}
	}
}

// Run is the source monitor: every SourceTimeout/2 it flips live -> stale when no frame arrived
// within SourceTimeout, and notifies every client. It returns ctx.Err(); afterwards register
// refuses new clients.
func (h *Hub) Run(ctx context.Context) error {
	defer func() {
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
	}()
	t := time.NewTicker(max(h.timeout/2, time.Nanosecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			h.checkStale()
		}
	}
}

// checkStale runs one monitor step.
func (h *Hub) checkStale() {
	s := h.state.Load()
	if s&stMask != stLive {
		return
	}
	n := h.frames.Load()
	seq := h.lastSeq.Load()
	if h.now()-time.Duration(h.lastFrame.Load()) < h.timeout {
		return
	}
	if h.beforeStaleCAS != nil {
		h.beforeStaleCAS()
	}
	// Store the cutoff before the state can read stale, so a hello snapshot that sees stale also
	// sees this cutoff. A reverted attempt leaves it raised, which only names frames that were
	// already a full timeout old.
	h.staleAtSeq.Store(seq)
	stale := ((s &^ stMask) + genOneInc) | stStale
	if !h.state.CompareAndSwap(s, stale) {
		return
	}
	if h.frames.Load() != n {
		// A frame raced the transition and may have observed live (so it did not flip the state
		// itself). Undo; if Publish already flipped it back, this CAS fails harmlessly.
		h.state.CompareAndSwap(stale, ((stale&^stMask)+genOneInc)|stLive)
		return
	}
	for _, c := range *h.clients.Load() {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

// sourceState returns the current source state.
func (h *Hub) sourceState() SourceState { return stateNames[h.state.Load()&stMask] }

// Stats returns a snapshot of the hub counters.
func (h *Hub) Stats() HubStats {
	return HubStats{
		Clients:        len(*h.clients.Load()),
		Accepted:       h.accepted.Load(),
		Sent:           h.sent.Load(),
		Dropped:        h.dropped.Load(),
		InvalidInputs:  h.invalid.Load(),
		ProjectErrors:  h.projErr.Load(),
		SlowClosed:     h.slowClosed.Load(),
		StaleDiscarded: h.staleDiscarded.Load(),
		State:          h.sourceState(),
	}
}

// register adds a client to the snapshot. The caller registers first and only then lets stream
// snapshot the state for hello, so no frame published in between is lost. It returns false once
// Run has returned. Capacity limits are the server's job.
func (h *Hub) register() (*client, bool) {
	c := &client{
		samples: make(chan *telemetryv1.TelemetryFrame, h.buf),
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	old := *h.clients.Load()
	next := make([]*client, len(old), len(old)+1)
	copy(next, old)
	next = append(next, c)
	h.clients.Store(&next)
	h.accepted.Add(1)
	return c, true
}

// unregister removes c from the snapshot and closes c.done. It never closes c.samples. Idempotent.
func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	old := *h.clients.Load()
	next := make([]*client, 0, len(old))
	for _, x := range old {
		if x != c {
			next = append(next, x)
		}
	}
	h.clients.Store(&next)
	h.mu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
}

// stream is the writer for one registered client: hello first, then status and sample messages in
// ADR 0002 order. send writes one text message and must honor its context's deadline; each write
// gets writeTimeout. It returns ctx.Err() on cancellation, nil after unregister, or the send error
// (the server then closes the connection). It always unregisters c.
func (h *Hub) stream(ctx context.Context, c *client, send func(context.Context, []byte) error, writeTimeout time.Duration) error {
	defer h.unregister(c)
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		if err := send(wctx, b); err != nil {
			if ctx.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || wctx.Err() != nil) {
				h.slowClosed.Add(1)
			}
			return err
		}
		h.sent.Add(1)
		return nil
	}

	lastSent := h.sourceState()
	// Samples with Sequence <= cutoff (when haveCut) are discarded as stale before projection or any
	// live status. The cutoff only rises. It applies to every dequeued frame, so a frame that a
	// stalled Publish enqueues after the stale handling below is still discarded.
	var cutoff uint64
	haveCut := false
	raiseCutoff := func() {
		if seq := h.staleAtSeq.Load(); !haveCut || seq > cutoff {
			cutoff, haveCut = seq, true
		}
	}
	if lastSent == StateStale {
		raiseCutoff() // staleAtSeq is stored before the state reads stale
	}
	if err := write(NewHello(h.sessionID, h.version, lastSent)); err != nil {
		return err
	}
	var prevSeq uint64
	havePrev := false

	// onStale handles a notify: a stale transition happened since the last one handled (notifies
	// coalesce). The status is sent even if the hub is already live again, so a segment break is
	// never lost. No status while waiting: the client never saw live.
	onStale := func() error {
		raiseCutoff()
		if lastSent != StateLive {
			return nil
		}
		lastSent = StateStale
		return write(NewStatus(h.sessionID, StateStale, ReasonStaleTimeout))
	}

	for {
		// Priority: shutdown and stale notifications go ahead of queued samples.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		case <-c.notify:
			if err := onStale(); err != nil {
				return err
			}
			continue
		default:
		}
		var f *telemetryv1.TelemetryFrame
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		case <-c.notify:
			if err := onStale(); err != nil {
				return err
			}
			continue
		case f = <-c.samples:
		}
		if haveCut && f.Sequence <= cutoff {
			h.staleDiscarded.Add(1)
			continue
		}
		s, invalid, err := Project(h.sessionID, f, DroppedSince(prevSeq, f.Sequence, havePrev))
		if err != nil {
			h.projErr.Add(1)
			continue
		}
		if lastSent != StateLive {
			reason := ReasonResumed
			if lastSent == StateWaiting {
				reason = ReasonFirstFrame
			}
			if err := write(NewStatus(h.sessionID, StateLive, reason)); err != nil {
				return err
			}
			lastSent = StateLive
		}
		h.invalid.Add(uint64(invalid))
		if err := write(s); err != nil {
			return err
		}
		prevSeq, havePrev = f.Sequence, true
	}
}
