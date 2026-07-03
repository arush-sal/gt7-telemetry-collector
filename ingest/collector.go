package ingest

import (
	"sync/atomic"
	"time"

	"github.com/arush-sal/garage/backend/collector/protocol"
	"github.com/arush-sal/garage/backend/collector/rawsink"
	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
	"github.com/arush-sal/garage/backend/telemetry"
)

// Publisher is the subset of the transport publisher the pipeline needs. Depending on this
// interface (rather than the concrete gRPC type) keeps ingest decoupled from transport and lets
// tests substitute a fake.
type Publisher interface {
	Publish(f *telemetryv1.TelemetryFrame)
}

// Pipeline runs one received packet through the full decode path and publishes the resulting
// canonical frame. Handle is meant to run inline on the single receive goroutine; the blocking
// stages (raw dump, publish) are non-blocking behind their own buffers. All drops are counted, not
// returned — the receive path must never stall or branch on per-packet errors.
type Pipeline struct {
	raw   rawsink.RawSink   // optional debug dump; nil when disabled
	dedup *protocol.Deduper // persistent dedup state
	pub   Publisher

	seq     atomic.Uint64 // frames published (also the frame sequence number)
	foreign atomic.Uint64 // decrypt/magic failures (not a GT7 packet)
	short   atomic.Uint64 // packets too short to decode
	stale   atomic.Uint64 // duplicates / small reorders dropped by dedup
	resets  atomic.Uint64 // session-reset packets accepted
	gaps    atomic.Uint64 // accepted packets preceded by a PackageID gap
}

// Counters is a snapshot of the pipeline's packet accounting.
type Counters struct {
	Published uint64
	Foreign   uint64
	Short     uint64
	Stale     uint64
	Resets    uint64
	Gaps      uint64
}

// NewPipeline builds a pipeline that publishes to pub and, if raw is non-nil, tees each raw
// (still-encrypted) packet to it for debugging.
func NewPipeline(raw rawsink.RawSink, pub Publisher) *Pipeline {
	dd := protocol.NewDeduper()
	return &Pipeline{raw: raw, dedup: &dd, pub: pub}
}

// Handle processes one received packet: tee raw -> decrypt -> validate -> dedup -> decode -> map ->
// publish. It never blocks and never returns an error; every rejection increments a counter.
func (p *Pipeline) Handle(payload []byte, recvTS time.Time) {
	if p.raw != nil {
		_ = p.raw.Write(recvTS, payload) // encrypted bytes, buffered; errors counted inside the sink
	}

	dec, err := protocol.Decrypt(payload)
	if err != nil {
		p.foreign.Add(1)
		return
	}
	if err := protocol.Validate(dec); err != nil {
		p.short.Add(1)
		return
	}

	id := protocol.PeekPackageID(dec) // peek before decoding (skip decode on stale packets)
	gap := p.dedup.Gap(id)
	accept, reset := p.dedup.Accept(id)
	if !accept {
		p.stale.Add(1)
		return
	}
	if reset {
		gap = false
		p.resets.Add(1)
	}
	if gap {
		p.gaps.Add(1)
	}

	d, err := protocol.Decode(dec)
	if err != nil {
		// Should not happen after Validate, but guard rather than publish garbage.
		p.short.Add(1)
		return
	}

	seq := p.seq.Add(1)
	frame := telemetry.ToCanonical(d, telemetry.CaptureMeta{
		RecvTime: recvTS,
		Sequence: seq,
		Gap:      gap,
		Source:   telemetryv1.Source_SOURCE_LIVE,
	})
	p.pub.Publish(frame)
}

// Counters returns a snapshot of the pipeline's packet accounting.
func (p *Pipeline) Counters() Counters {
	return Counters{
		Published: p.seq.Load(),
		Foreign:   p.foreign.Load(),
		Short:     p.short.Load(),
		Stale:     p.stale.Load(),
		Resets:    p.resets.Load(),
		Gaps:      p.gaps.Load(),
	}
}
