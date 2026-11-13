package rawsink

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/spf13/afero"
	"go.uber.org/zap"
)

// snapLen accommodates the largest IPv4 packet plus its Ethernet header.
const snapLen = 65535 + 14

// MaxPayload is the largest UDP payload an IPv4 datagram can carry (65535 - 20-byte IPv4 header -
// 8-byte UDP header). Larger payloads cannot be represented in the synthesized headers.
const MaxPayload = 65535 - 20 - 8

var errSinkClosed = errors.New("rawsink: write to closed sink")

// ErrPayloadTooLarge is returned by Write for payloads over MaxPayload; nothing is recorded.
var ErrPayloadTooLarge = errors.New("rawsink: payload exceeds MaxPayload")

type rawRecord struct {
	ts      time.Time
	payload []byte
}

// LocalSink is an afero-backed RawSink that writes nanosecond pcap records to a
// single file. A background goroutine performs the writes so Write never blocks on disk I/O; when
// the in-memory buffer is full, records are dropped and counted (acceptable for a debug-only dump).
type LocalSink struct {
	mu       sync.RWMutex // guards closed + sends on ch, so Close can't race a Write onto a closed chan
	closed   bool
	ch       chan rawRecord
	done     chan struct{}
	f        afero.File
	writer   *pcapgo.Writer
	src, dst netip.AddrPort

	dropped   atomic.Uint64 // records discarded because the buffer was full
	writeErrs atomic.Uint64 // background write failures (cannot be returned to Write)
	loggedErr atomic.Bool   // whether the first write error was already logged (avoid log spam)
	log       *zap.Logger
}

// NewLocalSink creates path's parent directory, opens (truncating) path for writing, and starts the
// background writer goroutine. bufSize bounds the in-memory record buffer.
// src and dst describe synthetic IPv4/UDP headers; an unknown dst IP becomes 0.0.0.0.
func NewLocalSink(fs afero.Fs, path string, bufSize int, src, dst netip.AddrPort) (*LocalSink, error) {
	if !dst.Addr().IsValid() || dst.Addr().IsUnspecified() {
		dst = netip.AddrPortFrom(netip.IPv4Unspecified(), dst.Port())
	}
	if !src.Addr().Unmap().Is4() || !dst.Addr().Unmap().Is4() {
		return nil, fmt.Errorf("rawsink: synthetic headers require IPv4 addresses")
	}
	if bufSize <= 0 {
		return nil, fmt.Errorf("rawsink: buffer size must be positive")
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := fs.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	writer := pcapgo.NewWriterNanos(f)
	if err := writer.WriteFileHeader(snapLen, layers.LinkTypeEthernet); err != nil {
		_ = f.Close()
		return nil, err
	}
	s := &LocalSink{
		writer: writer, src: src, dst: dst,
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
	// Ethernet addresses contain six octets; both synthetic MACs are zero.
	eth := &layers.Ethernet{SrcMAC: make(net.HardwareAddr, 6), DstMAC: make(net.HardwareAddr, 6), EthernetType: layers.EthernetTypeIPv4}
	// IPv4 version 4 and TTL 64 are conventional synthetic capture header values.
	ip := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP, SrcIP: s.src.Addr().AsSlice(), DstIP: s.dst.Addr().AsSlice()}
	udp := &layers.UDP{SrcPort: layers.UDPPort(s.src.Port()), DstPort: layers.UDPPort(s.dst.Port())}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		return err
	}
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, eth, ip, udp, gopacket.Payload(rec.payload)); err != nil {
		return err
	}
	data := buf.Bytes()
	return s.writer.WritePacket(gopacket.CaptureInfo{Timestamp: rec.ts, CaptureLength: len(data), Length: len(data)}, data)
}

// Write copies the payload and hands it to the writer goroutine without blocking. If the buffer is
// full it drops the record and increments the dropped counter.
func (s *LocalSink) Write(recvTS time.Time, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrPayloadTooLarge
	}
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
