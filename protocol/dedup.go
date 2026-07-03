package protocol

// Deduper decides which telemetry packets to keep by tracking PackageID. It drops duplicates and
// small out-of-order (stale) packets while accepting a large backward jump as a new-session reset.
// Its zero value is ready to use. It is not safe for concurrent use — the collector runs dedup
// inline on the single receive goroutine.
type Deduper struct {
	last uint32 // PackageID of the last accepted packet
	seen bool   // has any packet been accepted yet? (distinguishes cold start from last==0)
}

// Accept reports whether the packet with the given PackageID should be kept. It returns accept=true
// for the first packet, for normal forward progress, and for a session reset (a backward jump
// larger than resetThreshold, also signalled by reset=true); it returns accept=false for duplicates
// and small out-of-order packets. Accept mutates the Deduper and is not safe for concurrent use.
func (d *Deduper) Accept(id uint32) (accept bool, reset bool) {

	if !d.seen {
		d.seen = true
		d.last = id
		return true, false
	}

	if id > d.last {
		// normal forward progress: accept the packet
		d.last = id
		return true, false
	}

	if (id <= d.last) && (d.last-id < resetThreshold) {
		// small gap -> stale/duplicate/reorder:  drop the packet
		return false, false
	}

	// huge gap  -> counter reset (new session): accept the packet
	d.last = id
	return true, true
}

// Gap reports whether accepting id would leave a hole after the last accepted PackageID (i.e. one
// or more packets were lost). It is read-only and must be called before Accept, which updates the
// last-seen id. Duplicates, reorders, and resets are not gaps.
func (d *Deduper) Gap(id uint32) bool { return d.seen && id > d.last+1 }

// NewDeduper returns a ready-to-use Deduper. The zero value is already valid; this constructor
// exists for call-site clarity.
func NewDeduper() Deduper {
	return Deduper{}
}
