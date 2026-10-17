package replay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

// packetReader is the slice of the gopacket readers replay needs. Both pcapgo.Reader (.pcap) and
// pcapgo.NgReader (.pcapng) satisfy it.
type packetReader interface {
	ReadPacketData() ([]byte, gopacket.CaptureInfo, error)
	LinkType() layers.LinkType
}

// Source is an opened capture ready to Play. Close it when done.
type Source struct {
	r packetReader
	f *os.File
}

// isPcapMagic reports whether the leading 4 bytes are a classic libpcap file magic, in any of its
// four byte orders (microsecond/nanosecond precision x big/little endian). These are fixed values of
// the libpcap file format, not choices.
func isPcapMagic(m [4]byte) bool {
	switch binary.BigEndian.Uint32(m[:]) {
	case 0xA1B2C3D4, 0xD4C3B2A1, 0xA1B23C4D, 0x4D3CB2A1:
		return true
	default:
		return false
	}
}

// Open opens a capture and picks the reader by the file's MAGIC BYTES, not its extension (a pcapng
// saved under a .pcap name must still open). It fails on a missing file or an unrecognized format, so
// callers can reject a bad --replay-file at startup.
func Open(path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read capture magic: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	var r packetReader
	switch {
	case magic == [4]byte{0x0A, 0x0D, 0x0D, 0x0A}: // pcapng Section Header Block type
		r, err = pcapgo.NewNgReader(f, pcapgo.DefaultNgReaderOptions)
	case isPcapMagic(magic):
		r, err = pcapgo.NewReader(f)
	default:
		err = fmt.Errorf("unrecognized capture format (magic %x); want pcap or pcapng", magic)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Source{r: r, f: f}, nil
}

// Close closes the underlying capture file.
func (s *Source) Close() error { return s.f.Close() }

// Play reads the capture to the end, passing each UDP payload addressed to recvPort to handle with
// its capture timestamp, paced at the original capture cadence divided by speed (> 0; 2 = twice as
// fast). Off-port traffic (DNS, heartbeats to the send port, other UDP) is skipped before handle so
// pipeline counters match a live run. It returns nil at a clean end of file, ctx.Err() if cancelled,
// or a wrapped read error for a truncated/corrupt capture. Play runs on the caller's goroutine and,
// like the live receive loop, calls handle inline.
func (s *Source) Play(ctx context.Context, recvPort uint16, speed float64, handle func([]byte, time.Time)) error {
	linkType := s.r.LinkType()
	var prevTS int64
	haveTS := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, ci, err := s.r.ReadPacketData()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read capture: %w", err)
		}
		udp, _ := gopacket.NewPacket(data, linkType, gopacket.Lazy).Layer(layers.LayerTypeUDP).(*layers.UDP)
		if udp == nil || uint16(udp.DstPort) != recvPort || len(udp.Payload) == 0 {
			continue
		}
		ts := ci.Timestamp.UnixNano()
		if haveTS {
			if d := pace(prevTS, ts, speed); d > 0 {
				t := time.NewTimer(d)
				select {
				case <-ctx.Done():
					t.Stop()
					return ctx.Err()
				case <-t.C:
				}
			}
		}
		// Only move the pacing baseline forward: an out-of-order (earlier) timestamp must not become
		// prevTS, or the next in-order frame would sleep the same gap a second time.
		if !haveTS || ts > prevTS {
			prevTS, haveTS = ts, true
		}
		handle(udp.Payload, ci.Timestamp)
	}
}
