package replay

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arush-sal/garage/backend/collector/ingest"
	telemetryv1 "github.com/arush-sal/garage/backend/gen/gt7/telemetry/v1"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

func TestIsPcapMagic(t *testing.T) {
	// Classic libpcap magic in all four byte orders (microsecond + nanosecond, big + little endian).
	for _, m := range [][4]byte{
		{0xA1, 0xB2, 0xC3, 0xD4}, {0xD4, 0xC3, 0xB2, 0xA1}, {0xA1, 0xB2, 0x3C, 0x4D}, {0x4D, 0x3C, 0xB2, 0xA1},
	} {
		if !isPcapMagic(m) {
			t.Errorf("isPcapMagic(%x) = false, want true", m)
		}
	}
	// pcapng (Section Header Block type) and junk must not be taken for classic pcap.
	for _, m := range [][4]byte{{0x0A, 0x0D, 0x0D, 0x0A}, {0, 0, 0, 0}} {
		if isPcapMagic(m) {
			t.Errorf("isPcapMagic(%x) = true, want false", m)
		}
	}
}

// udpPacket serializes an Ethernet/IPv4/UDP frame with the given dst port and payload.
func udpPacket(t *testing.T, dstPort layers.UDPPort, payload []byte) []byte {
	t.Helper()
	eth := &layers.Ethernet{
		SrcMAC: net.HardwareAddr{1, 1, 1, 1, 1, 1}, DstMAC: net.HardwareAddr{2, 2, 2, 2, 2, 2},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolUDP,
		SrcIP: net.IP{10, 0, 0, 1}, DstIP: net.IP{10, 0, 0, 2},
	}
	udp := &layers.UDP{SrcPort: 40000, DstPort: dstPort}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, eth, ip, udp, gopacket.Payload(payload)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writePcapng writes packets into a pcapng file at path, spaced step apart in capture time.
func writePcapng(t *testing.T, path string, step time.Duration, pkts [][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := pcapgo.NewNgWriter(f, layers.LinkTypeEthernet)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pkts {
		ci := gopacket.CaptureInfo{Timestamp: time.Unix(1_700_000_000, 0).Add(time.Duration(i) * step), CaptureLength: len(p), Length: len(p)}
		if err := w.WritePacket(ci, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	ngPath := filepath.Join(dir, "cap.pcapng")
	writePcapng(t, ngPath, 0, [][]byte{udpPacket(t, 33740, []byte("hello-gt7"))})

	// A pcapng named .pcap must STILL open: dispatch is by magic bytes, not extension.
	pcapNamed := filepath.Join(dir, "cap.pcap")
	b, err := os.ReadFile(ngPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pcapNamed, b, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ngPath, pcapNamed} {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%s): %v", filepath.Base(path), err)
		}
		_ = s.Close()
	}

	junk := filepath.Join(dir, "junk.pcap")
	if err := os.WriteFile(junk, []byte{0, 1, 2, 3, 4, 5, 6, 7}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(junk); err == nil {
		t.Error("Open(junk) = nil error, want error")
	}
	if _, err := Open(filepath.Join(dir, "missing.pcapng")); err == nil {
		t.Error("Open(missing) = nil error, want error")
	}
}

type payloadRec struct {
	payloads [][]byte
	ts       []time.Time
}

func (r *payloadRec) handle(p []byte, ts time.Time) {
	r.payloads = append(r.payloads, p)
	r.ts = append(r.ts, ts)
}

// TestPlay_FiltersPortAndPaces: only datagrams to the recv port reach the handler, in capture order,
// stamped with their capture timestamps, and playback honours capture cadence / speed.
func TestPlay_FiltersPortAndPaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.pcapng")
	writePcapng(t, path, 40*time.Millisecond, [][]byte{
		udpPacket(t, 33740, []byte{1}),
		udpPacket(t, 40000, []byte{9}), // off-port: filtered before the handler
		udpPacket(t, 33740, []byte{2}),
		udpPacket(t, 33740, []byte{3}),
	})
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	rec := &payloadRec{}
	start := time.Now()
	if err := s.Play(context.Background(), 33740, 2.0, rec.handle); err != nil {
		t.Fatalf("Play: %v", err)
	}
	// Capture spans 120ms (first to last recv-port packet); at 2x that's >= 60ms of pacing.
	if el := time.Since(start); el < 55*time.Millisecond {
		t.Errorf("Play took %v, want >= ~60ms (paced at 2x)", el)
	}
	if len(rec.payloads) != 3 {
		t.Fatalf("handled %d payloads, want 3", len(rec.payloads))
	}
	for i, want := range []byte{1, 2, 3} {
		if !bytes.Equal(rec.payloads[i], []byte{want}) {
			t.Errorf("payload %d = %v, want [%d]", i, rec.payloads[i], want)
		}
	}
	if want := time.Unix(1_700_000_000, 0); !rec.ts[0].Equal(want) {
		t.Errorf("first ts = %v, want capture timestamp %v", rec.ts[0], want)
	}
}

// TestPlay_Cancel: a cancelled context stops playback promptly (mid dead-air) with ctx's error.
func TestPlay_Cancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.pcapng")
	writePcapng(t, path, time.Second, [][]byte{udpPacket(t, 33740, []byte{1}), udpPacket(t, 33740, []byte{2})})
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Play(ctx, 33740, 1.0, func([]byte, time.Time) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Play err = %v, want context.DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("Play returned after %v, want prompt return on cancel", el)
	}
}

// TestPlay_FixturePackets drives real GT7 fixture packets through the real collector pipeline: every
// distinct packet must decode and publish.
func TestPlay_FixturePackets(t *testing.T) {
	var pkts [][]byte
	for _, n := range []string{"packet_00.bin", "packet_01.bin", "packet_02.bin"} {
		b, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, udpPacket(t, 33740, b))
	}
	path := filepath.Join(t.TempDir(), "fx.pcapng")
	writePcapng(t, path, time.Millisecond, pkts)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	pub := &countPub{}
	p := ingest.NewPipeline(nil, pub)
	if err := s.Play(context.Background(), 33740, 1.0, p.Handle); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if c := p.Counters(); c.Published != 3 || pub.n != 3 {
		t.Errorf("published=%d (pub saw %d), want 3; counters=%+v", c.Published, pub.n, c)
	}
}

type countPub struct{ n int }

func (c *countPub) Publish(*telemetryv1.TelemetryFrame) { c.n++ }

// TestPlay_RealCapture replays a real 1.3MB type-C capture from the sibling supporting-data checkout
// when present (it is not copied into this repo). Skipped otherwise.
func TestPlay_RealCapture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "supporting-data", "raw-packet-captures", "type-c-capture.pcapng")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real capture not available: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p := ingest.NewPipeline(nil, &countPub{})
	if err := s.Play(context.Background(), 33740, 1000, p.Handle); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if c := p.Counters(); c.Valid == 0 {
		t.Errorf("no valid GT7 packets decoded from real capture: %+v", c)
	}
}

// TestPlay_OutOfOrderTimestampDoesNotResleep: a frame stamped earlier than its predecessor must not
// lower the pacing baseline, else the next in-order frame would re-sleep the gap already waited.
func TestPlay_OutOfOrderTimestampDoesNotResleep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.pcapng")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := pcapgo.NewNgWriter(f, layers.LinkTypeEthernet)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	// A=0, B=+300ms, C=+100ms (out of order), D=+400ms. Correct total wait is 400ms; the old
	// behaviour (baseline drops to C) waited 300 + 0 + 300 = 600ms.
	for i, off := range []time.Duration{0, 300 * time.Millisecond, 100 * time.Millisecond, 400 * time.Millisecond} {
		p := udpPacket(t, 33740, []byte{byte(i)})
		ci := gopacket.CaptureInfo{Timestamp: base.Add(off), CaptureLength: len(p), Length: len(p)}
		if err := w.WritePacket(ci, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	if err := s.Play(context.Background(), 33740, 1.0, func([]byte, time.Time) {}); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if el := time.Since(start); el < 380*time.Millisecond || el > 520*time.Millisecond {
		t.Errorf("Play took %v, want ~400ms (no re-sleep after out-of-order frame)", el)
	}
}
