package rawsink

import "time"

// RawSink persists raw (still-encrypted) GT7 packets for offline debugging. Implementations buffer
// internally so Write never blocks the receive path; disk errors are counted, not returned.
type RawSink interface {
	// Write enqueues one packet with its receive timestamp. It never blocks on disk I/O and copies
	// the payload, so callers may reuse their buffer as soon as it returns.
	Write(recvTS time.Time, payload []byte) error
	// Close flushes buffered records and releases the underlying file. It is idempotent.
	Close() error
}
