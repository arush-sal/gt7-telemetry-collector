package rawsink

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/replay"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/spf13/afero"
)

func TestLocalSinkRoundTrip(t *testing.T) {
	for _, dst := range []netip.AddrPort{netip.MustParseAddrPort("192.0.2.10:33740"), netip.AddrPortFrom(netip.Addr{}, 33740)} {
		t.Run(dst.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.pcap")
			src := netip.MustParseAddrPort("192.0.2.5:33739")
			s, err := NewLocalSink(afero.NewOsFs(), path, 16, src, dst)
			if err != nil {
				t.Fatal(err)
			}
			payloads := [][]byte{bytes.Repeat([]byte{42}, 368), []byte("foreign"), {0xff}, {}}
			base := time.Unix(1700000000, 123456789)
			for i, p := range payloads {
				if err := s.Write(base.Add(time.Duration(i)), p); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if s.Dropped() != 0 {
				t.Fatal("unexpected dropped records")
			}
			source, err := replay.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			n := 0
			if err := source.Play(context.Background(), 33740, math.Inf(1), func(p []byte, ts time.Time) {
				if n >= len(payloads) {
					t.Fatal("extra packet")
				}
				if !bytes.Equal(p, payloads[n]) || !ts.Equal(base.Add(time.Duration(n))) {
					t.Errorf("packet %d: payload=%x timestamp=%v", n, p, ts)
				}
				n++
			}); err != nil {
				t.Fatal(err)
			}
			if n != len(payloads) {
				t.Fatalf("got %d packets", n)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			r, err := pcapgo.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			if r.LinkType() != layers.LinkTypeEthernet {
				t.Fatal("wrong link type")
			}
			b, _, err := r.ReadPacketData()
			if err != nil {
				t.Fatal(err)
			}
			packet := gopacket.NewPacket(b, layers.LinkTypeEthernet, gopacket.Default)
			eth := packet.Layer(layers.LayerTypeEthernet).(*layers.Ethernet)
			ip := packet.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
			udp := packet.Layer(layers.LayerTypeUDP).(*layers.UDP)
			wantDst := dst.Addr()
			if !wantDst.IsValid() {
				wantDst = netip.IPv4Unspecified()
			}
			if !bytes.Equal(eth.SrcMAC, make([]byte, 6)) || !bytes.Equal(eth.DstMAC, make([]byte, 6)) || ip.SrcIP.String() != src.Addr().String() || ip.DstIP.String() != wantDst.String() || ip.TTL != 64 || ip.Protocol != layers.IPProtocolUDP || uint16(udp.SrcPort) != src.Port() || uint16(udp.DstPort) != dst.Port() {
				t.Fatalf("wrong synthetic headers: %v", packet)
			}
			var sum uint32
			for i := 0; i < len(ip.Contents); i += 2 {
				sum += uint32(ip.Contents[i])<<8 | uint32(ip.Contents[i+1])
			}
			for sum>>16 != 0 {
				sum = (sum & 0xffff) + (sum >> 16)
			}
			if sum != 0xffff {
				t.Fatalf("invalid IPv4 checksum: %x", sum)
			}
		})
	}
}

func TestLocalSinkWriteAfterClose(t *testing.T) {
	s, err := NewLocalSink(afero.NewMemMapFs(), "raw.pcap", 4, netip.MustParseAddrPort("192.0.2.5:33739"), netip.MustParseAddrPort("0.0.0.0:33740"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(time.Now(), []byte{1}); err == nil {
		t.Fatal("Write after Close should fail")
	}
}

// TestLocalSinkRejectsOversizedPayload: a payload that cannot fit an IPv4/UDP datagram is refused
// up front instead of writing a record that would corrupt the rest of the capture on replay.
func TestLocalSinkRejectsOversizedPayload(t *testing.T) {
	s, err := NewLocalSink(afero.NewMemMapFs(), "raw.pcap", 4, netip.MustParseAddrPort("192.0.2.5:33739"), netip.MustParseAddrPort("0.0.0.0:33740"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Write(time.Unix(0, 1), make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("Write(oversized) = %v, want ErrPayloadTooLarge", err)
	}
	if err := s.Write(time.Unix(0, 1), make([]byte, MaxPayload)); err != nil {
		t.Fatalf("Write(MaxPayload) = %v, want nil", err)
	}
}
