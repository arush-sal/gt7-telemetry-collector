package discover

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

var loopback = netip.MustParseAddr("127.0.0.1")

// udp binds a 127.0.0.1 ephemeral socket closed at test end.
func udp(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func port(c *net.UDPConn) int { return c.LocalAddr().(*net.UDPAddr).Port }

// fake answers every datagram it receives with replies, sent back to the sender's address.
func fake(t *testing.T, replies ...[]byte) *net.UDPConn {
	t.Helper()
	c := udp(t)
	go func() {
		buf := make([]byte, 4096)
		for {
			_, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			for _, r := range replies {
				_, _ = c.WriteToUDPAddrPort(r, from)
			}
		}
	}()
	return c
}

func gt7Packet(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../protocol/testdata/packet_00.bin")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixed injects the given step targets (own addresses empty so the loopback fakes count).
func fixed(bcast, sweep []netip.Addr) func() ([]netip.Addr, []netip.Addr, []netip.Addr, error) {
	return func() ([]netip.Addr, []netip.Addr, []netip.Addr, error) { return bcast, sweep, nil, nil }
}

// closedPort returns a 127.0.0.1 UDP port nobody listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := port(c)
	_ = c.Close()
	return p
}

func TestBroadcast(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		pfx, want string
		ok        bool
	}{
		{"192.168.1.0/24", "192.168.1.255", true},
		{"10.0.0.0/23", "10.0.1.255", true},
		{"172.16.0.0/16", "172.16.255.255", true},
		{"192.168.1.4/31", "", false},
		{"192.168.1.4/32", "", false},
	} {
		got, ok := broadcast(netip.MustParsePrefix(tt.pfx))
		if ok != tt.ok || (ok && got.String() != tt.want) {
			t.Errorf("broadcast(%s) = %v, %v; want %s, %v", tt.pfx, got, ok, tt.want, tt.ok)
		}
	}
}

func TestHosts(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		self, pfx   string
		n           int
		first, last string
	}{
		{"192.168.1.10", "192.168.1.0/24", 253, "192.168.1.1", "192.168.1.254"},
		{"10.0.1.7", "10.0.0.0/23", 253, "10.0.1.1", "10.0.1.254"},         // wider than /24: clamp to self's /24
		{"172.16.5.9", "172.16.0.0/16", 253, "172.16.5.1", "172.16.5.254"}, // /16 clamp
		{"192.168.1.1", "192.168.1.0/30", 1, "192.168.1.2", "192.168.1.2"},
		{"192.168.1.4", "192.168.1.4/31", 0, "", ""},
		{"192.168.1.4", "192.168.1.4/32", 0, "", ""},
	} {
		self := netip.MustParseAddr(tt.self)
		got := hosts(self, netip.MustParsePrefix(tt.pfx))
		if len(got) != tt.n {
			t.Errorf("hosts(%s, %s) = %d hosts, want %d", tt.self, tt.pfx, len(got), tt.n)
			continue
		}
		for _, a := range got {
			if a == self {
				t.Errorf("hosts(%s, %s) contains self", tt.self, tt.pfx)
			}
		}
		if tt.n > 0 && (got[0].String() != tt.first || got[len(got)-1].String() != tt.last) {
			t.Errorf("hosts(%s, %s) = %v..%v, want %s..%s", tt.self, tt.pfx, got[0], got[len(got)-1], tt.first, tt.last)
		}
	}
}

func TestFilterNets(t *testing.T) {
	t.Parallel()
	up := net.FlagUp | net.FlagRunning | net.FlagBroadcast
	p := netip.MustParsePrefix
	got := filterNets([]ifaceAddrs{
		{up, []netip.Prefix{p("192.168.1.10/24"), p("fe80::1/64"), p("169.254.3.4/16")}},
		{up, []netip.Prefix{p("192.168.1.11/24")}},                                                // duplicate prefix
		{up | net.FlagLoopback, []netip.Prefix{p("127.0.0.1/8")}},                                 // loopback
		{net.FlagUp | net.FlagRunning | net.FlagPointToPoint, []netip.Prefix{p("100.64.0.2/24")}}, // tunnel
		{net.FlagUp | net.FlagBroadcast, []netip.Prefix{p("10.1.0.2/24")}},                        // not running
		{net.FlagRunning | net.FlagBroadcast, []netip.Prefix{p("10.2.0.2/24")}},                   // down
		{up, []netip.Prefix{p("::ffff:10.3.0.2/120")}},                                            // mapped v4
	})
	want := []localNet{
		{netip.MustParseAddr("192.168.1.10"), p("192.168.1.0/24")},
		{netip.MustParseAddr("10.3.0.2"), p("10.3.0.0/24")},
	}
	if len(got) != len(want) {
		t.Fatalf("filterNets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filterNets[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRouteFirst(t *testing.T) {
	t.Parallel()
	a := netip.MustParseAddr
	p := netip.MustParsePrefix
	docker := localNet{a("172.17.0.1"), p("172.17.0.0/16")}
	lan := localNet{a("172.16.1.99"), p("172.16.1.0/24")}
	other := localNet{a("10.0.0.2"), p("10.0.0.0/24")}
	got := routeFirst([]localNet{docker, other, lan}, a("172.16.1.99"))
	want := []localNet{lan, docker, other}
	if !slices.Equal(got, want) {
		t.Errorf("routeFirst = %v, want %v", got, want)
	}
	if got := routeFirst([]localNet{docker, lan}, netip.Addr{}); !slices.Equal(got, []localNet{docker, lan}) {
		t.Errorf("routeFirst without route = %v, want order kept", got)
	}
}

func TestParseSRCH(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, in, state string
		ok              bool
	}{
		{"ready", "HTTP/1.1 200 Ok\nhost-id:ABC\nhost-type:PS5\nhost-name:Living Room\nrunning-app-name:Gran Turismo 7\n\x00", "ready", true},
		{"standby", "HTTP/1.1 620 Server Standby\r\nhost-type:PS5\r\nhost-name:Living Room\r\n", "standby", true},
		{"garbage", "hello", "", false},
		{"other code", "HTTP/1.1 404 Not Found\nhost-name:x\n", "", false},
		{"empty", "", "", false},
	} {
		got, ok := parseSRCH([]byte(tt.in))
		if ok != tt.ok {
			t.Errorf("%s: ok = %v, want %v", tt.name, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.State != tt.state || got.Name != "Living Room" || got.Type != "PS5" {
			t.Errorf("%s: got %+v", tt.name, got)
		}
		if tt.state == "ready" && got.App != "Gran Turismo 7" {
			t.Errorf("%s: app = %q", tt.name, got.App)
		}
	}
}

func TestSRCHPayload(t *testing.T) {
	t.Parallel()
	want := "SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n\x00"
	if got := string(srchPayload("00030010")); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestProbeFindsGT7AmongGarbage(t *testing.T) {
	t.Parallel()
	pkt := gt7Packet(t)
	// Garbage, then right length but no magic, then a real GT7 packet.
	gt7 := fake(t, []byte("hello"), make([]byte, len(pkt)), pkt)
	conn := udp(t)
	got, ok, err := probe(context.Background(), conn, []netip.AddrPort{netip.AddrPortFrom(loopback, uint16(port(gt7)))}, 5*time.Second, nil, zap.NewNop())
	if err != nil || !ok || got != loopback || !got.Is4() {
		t.Fatalf("probe = %v (is4 %v), %v, %v; want %v", got, got.Is4(), ok, err, loopback)
	}
}

func TestProbeIgnoresInvalidAndOwn(t *testing.T) {
	t.Parallel()
	pkt := gt7Packet(t)
	for _, tt := range []struct {
		name  string
		reply []byte
		own   []netip.Addr
	}{
		{"garbage only", []byte("garbage"), nil},
		{"non-magic only", make([]byte, len(pkt)), nil},
		{"own address", pkt, []netip.Addr{loopback}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fake(t, tt.reply)
			start := time.Now()
			_, ok, err := probe(context.Background(), udp(t), []netip.AddrPort{netip.AddrPortFrom(loopback, uint16(port(f)))}, 300*time.Millisecond, tt.own, zap.NewNop())
			if err != nil || ok {
				t.Fatalf("probe ok=%v err=%v, want timeout false", ok, err)
			}
			if el := time.Since(start); el > 5*time.Second {
				t.Fatalf("probe took %v", el)
			}
		})
	}
}

func TestFindSweep(t *testing.T) {
	t.Parallel()
	gt7 := fake(t, gt7Packet(t))
	o := Options{SendPort: port(gt7), Timeout: 9 * time.Second, Log: zap.NewNop(),
		targets: fixed([]netip.Addr{netip.MustParseAddr("127.0.0.2")}, []netip.Addr{loopback}), srchPorts: [2]int{closedPort(t), closedPort(t)}}
	res, err := Find(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Addr != loopback || !res.Addr.Is4() || res.Method != "sweep" {
		t.Fatalf("Find = %+v, want %v via sweep", res, loopback)
	}
}

func TestFindBroadcast(t *testing.T) {
	t.Parallel()
	gt7 := fake(t, gt7Packet(t))
	o := Options{SendPort: port(gt7), Timeout: 9 * time.Second,
		targets: fixed([]netip.Addr{loopback}, nil), srchPorts: [2]int{closedPort(t), closedPort(t)}}
	res, err := Find(context.Background(), o)
	if err != nil || res.Addr != loopback || res.Method != "broadcast" {
		t.Fatalf("Find = %+v, %v; want %v via broadcast", res, err, loopback)
	}
}

func TestFindReleasesRecvPort(t *testing.T) {
	t.Parallel()
	gt7 := fake(t, gt7Packet(t))
	recv := closedPort(t)
	o := Options{SendPort: port(gt7), RecvPort: recv, Timeout: 9 * time.Second,
		targets: fixed(nil, []netip.Addr{loopback}), srchPorts: [2]int{closedPort(t), closedPort(t)}}
	if _, err := Find(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: recv})
	if err != nil {
		t.Fatalf("rebind recv port after Find: %v", err)
	}
	_ = c.Close()
}

func TestFindSRCHOnly(t *testing.T) {
	t.Parallel()
	var got []byte
	ps := udp(t)
	seen := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, from, err := ps.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		seen <- bytes.Clone(buf[:n])
		_, _ = ps.WriteToUDPAddrPort([]byte("HTTP/1.1 620 Server Standby\nhost-type:PS5\nhost-name:Den\nhost-id:XYZ\n"), from)
	}()
	o := Options{SendPort: closedPort(t), Timeout: 3 * time.Second,
		targets: fixed([]netip.Addr{loopback}, []netip.Addr{loopback}), srchPorts: [2]int{port(ps), closedPort(t)}}
	_, err := Find(context.Background(), o)
	var ns *NotStreamingError
	if !errors.As(err, &ns) {
		t.Fatalf("Find err = %v, want *NotStreamingError", err)
	}
	if ns.Addr != loopback || ns.Name != "Den" || ns.Type != "PS5" || ns.State != "standby" {
		t.Fatalf("NotStreamingError = %+v", ns)
	}
	for _, s := range []string{"Den", "standby", "127.0.0.1", "--ps5", "GT7"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q missing %q", err, s)
		}
	}
	got = <-seen
	if want := string(srchPayload("00030010")); string(got) != want {
		t.Errorf("PS5 SRCH request = %q, want %q", got, want)
	}
}

func TestFindNotFoundWithinBudget(t *testing.T) {
	t.Parallel()
	silent := udp(t) // receives, never replies
	budget := 900 * time.Millisecond
	o := Options{SendPort: port(silent), Timeout: budget,
		targets: fixed([]netip.Addr{loopback}, []netip.Addr{loopback}), srchPorts: [2]int{port(silent), port(silent)}}
	start := time.Now()
	_, err := Find(context.Background(), o)
	el := time.Since(start)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "--ps5") {
		t.Fatalf("Find err = %v, want ErrNotFound mentioning --ps5", err)
	}
	if el < budget-100*time.Millisecond || el > budget+3*time.Second {
		t.Fatalf("Find took %v, budget %v", el, budget)
	}
}

func TestFindCancel(t *testing.T) {
	t.Parallel()
	silent := udp(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	o := Options{SendPort: port(silent), Timeout: time.Minute,
		targets: fixed([]netip.Addr{loopback}, []netip.Addr{loopback}), srchPorts: [2]int{port(silent), port(silent)}}
	start := time.Now()
	_, err := Find(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Find err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("cancel took %v", el)
	}
}

func TestFindContextDeadline(t *testing.T) {
	t.Parallel()
	silent := udp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	o := Options{SendPort: port(silent), Timeout: time.Minute,
		targets: fixed([]netip.Addr{loopback}, []netip.Addr{loopback}), srchPorts: [2]int{port(silent), port(silent)}}
	_, err := Find(ctx, o)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Find err = %v, want context.DeadlineExceeded", err)
	}
}

func TestFindBindError(t *testing.T) {
	t.Parallel()
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	o := Options{SendPort: closedPort(t), RecvPort: port(busy), Timeout: 2 * time.Second,
		targets: fixed([]netip.Addr{loopback}, nil), srchPorts: [2]int{closedPort(t), closedPort(t)}}
	_, err = Find(context.Background(), o)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "bind") {
		t.Fatalf("Find err = %v, want bind error", err)
	}
}
