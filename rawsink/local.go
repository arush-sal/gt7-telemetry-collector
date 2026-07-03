package rawsink

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"
	"go.uber.org/zap"
)

// recordHeaderSize is the fixed per-record header written before each payload:
// a little-endian uint32 payload length followed by a little-endian int64 unix-nanos timestamp.
const recordHeaderSize = 12

var errSinkClosed = errors.New("rawsink: write to closed sink")

type rawRecord struct {
	ts      time.Time
	payload []byte
}

// LocalSink is an afero-backed RawSink that appends length-prefixed, timestamped records to a
// single file. A background goroutine performs the writes so Write never blocks on disk I/O; when
// the in-memory buffer is full, records are dropped and counted (acceptable for a debug-only dump).
type LocalSink struct {
	mu     sync.RWMutex // guards closed + sends on ch, so Close can't race a Write onto a closed chan
	closed bool
	ch     chan rawRecord
	done   chan struct{}
	f      afero.File

	dropped   atomic.Uint64 // records discarded because the buffer was full
	writeErrs atomic.Uint64 // background write failures (cannot be returned to Write)
	loggedErr atomic.Bool   // whether the first write error was already logged (avoid log spam)
	log       *zap.Logger
}

// NewLocalSink creates path's parent directory, opens (truncating) path for writing, and starts the
// background writer goroutine. bufSize bounds the in-memory record buffer.
func NewLocalSink(fs afero.Fs, path string, bufSize int) (*LocalSink, error) {
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := fs.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	s := &LocalSink{
		ch:   make(chan rawRecord, bufSize),
		done: make(chan struct{}),
		f:    f,
		log:  zap.NewNop(),
	}
	go s.run()
	return s, nil
}

// SetLogger attaches a logger used to report background write failures. Optional; the zero logger
// is a no-op. Call before the sink is used.
func (s *LocalSink) SetLogger(log *zap.Logger) { s.log = log }

// run drains the buffer and writes each record until the channel is closed by Close.
func (s *LocalSink) run() {
	defer close(s.done)
	for rec := range s.ch {
		if err := s.writeRecord(rec); err != nil {
			s.writeErrs.Add(1)
			// Log only the first failure to avoid flooding when the disk is persistently failing.
			if s.loggedErr.CompareAndSwap(false, true) {
				s.log.Warn("raw dump write failed; further errors counted only", zap.Error(err))
			}
		}
	}
}

func (s *LocalSink) writeRecord(rec rawRecord) error {
	var hdr [recordHeaderSize]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(rec.payload)))
	binary.LittleEndian.PutUint64(hdr[4:12], uint64(rec.ts.UnixNano()))
	if _, err := s.f.Write(hdr[:]); err != nil {
		return err
	}
	_, err := s.f.Write(rec.payload)
	return err
}

// Write copies the payload and hands it to the writer goroutine without blocking. If the buffer is
// full it drops the record and increments the dropped counter.
func (s *LocalSink) Write(recvTS time.Time, payload []byte) error {
	cp := make([]byte, len(payload))
	copy(cp, payload)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errSinkClosed
	}
	select {
	case s.ch <- rawRecord{ts: recvTS, payload: cp}:
	default:
		s.dropped.Add(1)
	}
	return nil
}

// Close stops accepting writes, drains buffered records, and closes the file. It is idempotent.
func (s *LocalSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()

	<-s.done
	return s.f.Close()
}

// Dropped returns the number of records discarded because the buffer was full.
func (s *LocalSink) Dropped() uint64 { return s.dropped.Load() }
