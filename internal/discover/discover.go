// Package discover finds a GT7 console on the directly attached IPv4 subnets. It sends the GT7
// heartbeat to each subnet broadcast address, then unicast to every host, and accepts the first
// reply that decrypts as GT7 telemetry. If neither answers it falls back to a PlayStation SRCH
// broadcast, which can name the console but cannot prove GT7 is streaming.
//
// Telemetry is returned to the heartbeat's apparent source, so only subnets reachable without
// source NAT or tunnels can work; point-to-point interfaces are skipped.
package discover

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/protocol"
	"go.uber.org/zap"
)

const (
	ps5SRCHPort = 9302
	ps4SRCHPort = 987
	// ponytail: cap sized to the Linux neighbour table (gc_thresh3=1024); add a flag if someone
	// needs a bigger LAN swept.
	maxSweep   = 512
	sendBatch  = 32
	batchGap   = 2 * time.Millisecond
	resendTick = time.Second
)

// ErrNotFound reports that no console answered within the discovery budget.
var ErrNotFound = errors.New("discover: no GT7 console answered on the local subnets; start GT7 or pass --ps5")

// NotStreamingError reports a PlayStation that answered SRCH while GT7 sent no telemetry.
type NotStreamingError struct {
	Addr                   netip.Addr
	Name, Type, State, App string
}

func (e *NotStreamingError) Error() string {
	app := ""
	if e.App != "" {
		app = fmt.Sprintf(", running %q", e.App)
	}
	return fmt.Sprintf("discover: found %s %q (%s%s) at %s but GT7 is not streaming telemetry; start GT7 or pass --ps5",
		e.Type, e.Name, e.State, app, e.Addr)
}

// Result is a console that sent GT7 telemetry, and the step ("broadcast" or "sweep") that found it.
type Result struct {
	Addr   netip.Addr
	Method string
}

// Options configures Find. SendPort and RecvPort are the GT7 heartbeat and telemetry ports.
type Options struct {
	SendPort, RecvPort int
	Timeout            time.Duration // total budget for all steps
	Log                *zap.Logger   // nil = no logging

	// targets returns broadcast addresses, sweep hosts and own addresses; nil = the host's
	// interfaces. srchPorts overrides the PS5/PS4 SRCH ports; zero = 9302/987. Test seams.
	targets   func() (bcast, sweep, own []netip.Addr, err error)
	srchPorts [2]int
}

// Find runs the discovery steps in order, splitting the remaining budget evenly between the steps
// still to run. Bind and I/O errors are returned as such; cancelling ctx returns ctx.Err().
func Find(ctx context.Context, o Options) (Result, error) {
	log := o.Log
	if log == nil {
		log = zap.NewNop()
	}
	targets, ports := o.targets, o.srchPorts
	if targets == nil {
		targets = systemTargets
	}
	if ports == [2]int{} {
		ports = [2]int{ps5SRCHPort, ps4SRCHPort}
	}
	bcast, sweep, own, err := targets()
	if err != nil {
		return Result{}, fmt.Errorf("discover: interfaces: %w", err)
	}
	log.Debug("discovery targets", zap.Int("broadcast", len(bcast)), zap.Int("sweep", len(sweep)))
	deadline := time.Now().Add(o.Timeout)

	// Same port as the Receiver: GT7 replies to the heartbeat's source port.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: o.RecvPort})
	if err != nil {
		return Result{}, fmt.Errorf("discover: bind :%d: %w", o.RecvPort, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	for i, step := range []struct {
		method string
		hosts  []netip.Addr
	}{{"broadcast", bcast}, {"sweep", sweep}} {
		var to []netip.AddrPort
		for _, h := range step.hosts {
			to = append(to, netip.AddrPortFrom(h, uint16(o.SendPort)))
		}
		addr, ok, err := probe(ctx, conn, to, time.Until(deadline)/time.Duration(3-i), own, log)
		if err != nil || ok {
			stop()
			_ = conn.Close()
			return Result{Addr: addr, Method: step.method}, err
		}
	}
	stop()
	_ = conn.Close() // released before returning so the Receiver can bind RecvPort

	c, ok, err := srch(ctx, bcast, ports, time.Until(deadline), log)
	switch {
	case err != nil:
		return Result{}, err
	case ok:
		return Result{}, &c
	}
	return Result{}, ErrNotFound
}

// probe sends the GT7 heartbeat to targets and returns the first source whose reply decrypts as a
// GT7 packet, skipping own addresses. ok is false when wait expires first.
func probe(ctx context.Context, conn *net.UDPConn, targets []netip.AddrPort, wait time.Duration, own []netip.Addr, log *zap.Logger) (netip.Addr, bool, error) {
	pkts := make([]packet, len(targets))
	for i, t := range targets {
		pkts[i] = packet{t, protocol.HeartbeatPayload()}
	}
	return exchange(ctx, conn, pkts, wait, func(src netip.Addr, b []byte) bool {
		_, err := protocol.Decrypt(b)
		return !slices.Contains(own, src) && err == nil
	}, log)
}

// srch broadcasts the PS5 and PS4 SRCH requests to bcast and parses the first valid reply.
func srch(ctx context.Context, bcast []netip.Addr, ports [2]int, wait time.Duration, log *zap.Logger) (NotStreamingError, bool, error) {
	conn, err := net.ListenUDP("udp4", nil) // Go enables SO_BROADCAST on UDP sockets
	if err != nil {
		return NotStreamingError{}, false, fmt.Errorf("discover: srch bind: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var pkts []packet
	for _, b := range bcast {
		pkts = append(pkts, packet{netip.AddrPortFrom(b, uint16(ports[0])), srchPayload("00030010")},
			packet{netip.AddrPortFrom(b, uint16(ports[1])), srchPayload("00020020")})
	}
	var c NotStreamingError
	addr, ok, err := exchange(ctx, conn, pkts, wait, func(_ netip.Addr, b []byte) bool {
		var ok bool
		c, ok = parseSRCH(b)
		return ok
	}, log)
	c.Addr = addr
	return c, ok, err
}

type packet struct {
	to   netip.AddrPort
	data []byte
}

// exchange sends pkts (paced, resent every second) while reading replies until accept passes one,
// wait expires (ok false), ctx ends (ctx.Err()) or a read fails.
func exchange(ctx context.Context, conn *net.UDPConn, pkts []packet, wait time.Duration, accept func(netip.Addr, []byte) bool, log *zap.Logger) (netip.Addr, bool, error) {
	if ctx.Err() != nil || len(pkts) == 0 || wait <= 0 {
		return netip.Addr{}, false, ctx.Err()
	}
	end := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(end) {
		end = d
	}
	if err := conn.SetDeadline(end); err != nil { // write deadline too: a blocked send must not outlive the step
		return netip.Addr{}, false, errors.Join(ctx.Err(), err)
	}
	sendCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	wg.Go(func() {
		for {
			for i, p := range pkts {
				if i > 0 && i%sendBatch == 0 && !sleep(sendCtx, batchGap) {
					return
				}
				if _, err := conn.WriteToUDPAddrPort(p.data, p.to); err != nil {
					log.Debug("discovery send failed", zap.Stringer("to", p.to), zap.Error(err))
				}
			}
			if !sleep(sendCtx, resendTick) {
				return
			}
		}
	})
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return netip.Addr{}, false, ctx.Err()
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return netip.Addr{}, false, nil
			}
			return netip.Addr{}, false, fmt.Errorf("discover: read: %w", err)
		}
		src := from.Addr().Unmap()
		if accept(src, buf[:n]) {
			return src, true, nil
		}
		log.Debug("discovery ignored datagram", zap.Stringer("from", src), zap.Int("bytes", n))
	}
}

// sleep waits d and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// srchPayload is the PlayStation device-discovery request; the console expects the trailing NUL.
func srchPayload(version string) []byte {
	return []byte("SRCH * HTTP/1.1\ndevice-discovery-protocol-version:" + version + "\n\x00")
}

// parseSRCH parses an "HTTP/1.1 <code> <reason>" reply with key:value headers. Only 200 (ready)
// and 620 (standby) are accepted.
func parseSRCH(b []byte) (NotStreamingError, bool) {
	lines := strings.Split(strings.TrimRight(string(b), "\x00"), "\n")
	f := strings.Fields(lines[0])
	if len(f) < 2 || f[0] != "HTTP/1.1" {
		return NotStreamingError{}, false
	}
	var c NotStreamingError
	switch code, _ := strconv.Atoi(f[1]); code {
	case 200:
		c.State = "ready"
	case 620:
		c.State = "standby"
	default:
		return NotStreamingError{}, false
	}
	for _, l := range lines[1:] {
		k, v, ok := strings.Cut(strings.TrimRight(l, "\r"), ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "host-name":
			c.Name = strings.TrimSpace(v)
		case "host-type":
			c.Type = strings.TrimSpace(v)
		case "running-app-name":
			c.App = strings.TrimSpace(v)
		}
	}
	return c, true
}

type localNet struct {
	self netip.Addr
	pfx  netip.Prefix
}

type ifaceAddrs struct {
	flags net.Flags
	addrs []netip.Prefix
}

// filterNets keeps the IPv4, non-link-local subnets of up, running, broadcast-capable interfaces
// that are neither loopback nor point-to-point (tun, WireGuard, Tailscale), deduplicated by prefix.
func filterNets(ifs []ifaceAddrs) []localNet {
	var out []localNet
	for _, ifc := range ifs {
		want := net.FlagUp | net.FlagRunning | net.FlagBroadcast
		if ifc.flags&want != want || ifc.flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		for _, p := range ifc.addrs {
			a, bits := p.Addr(), p.Bits()
			if a.Is4In6() {
				a, bits = a.Unmap(), bits-96
			}
			if !a.Is4() || a.IsLinkLocalUnicast() || bits < 0 {
				continue
			}
			pfx := netip.PrefixFrom(a, bits).Masked()
			if !slices.ContainsFunc(out, func(n localNet) bool { return n.pfx == pfx }) {
				out = append(out, localNet{a, pfx})
			}
		}
	}
	return out
}

// routeFirst moves the subnet holding the default-route source address to the front, so it is
// swept first and never cut by maxSweep (e.g. by a Docker bridge listed earlier).
func routeFirst(nets []localNet, src netip.Addr) []localNet {
	out := slices.Clone(nets)
	slices.SortStableFunc(out, func(a, b localNet) int {
		ac, bc := a.pfx.Contains(src), b.pfx.Contains(src)
		switch {
		case ac && !bc:
			return -1
		case bc && !ac:
			return 1
		}
		return 0
	})
	return out
}

// broadcast returns the last address of an IPv4 prefix; /31 and /32 have none.
func broadcast(pfx netip.Prefix) (netip.Addr, bool) {
	if !pfx.Addr().Is4() || pfx.Bits() > 30 {
		return netip.Addr{}, false
	}
	a := pfx.Masked().Addr().As4()
	u := binary.BigEndian.Uint32(a[:]) | (1<<(32-pfx.Bits()) - 1)
	binary.BigEndian.PutUint32(a[:], u)
	return netip.AddrFrom4(a), true
}

// hosts lists every host of pfx except the network, broadcast and self addresses. A prefix wider
// than /24 is clamped to the /24 containing self.
func hosts(self netip.Addr, pfx netip.Prefix) []netip.Addr {
	if pfx.Bits() < 24 {
		pfx = netip.PrefixFrom(self, 24)
	}
	bc, ok := broadcast(pfx)
	if !ok {
		return nil
	}
	var out []netip.Addr
	for a := pfx.Masked().Addr().Next(); a != bc; a = a.Next() {
		if a != self {
			out = append(out, a)
		}
	}
	return out
}

// systemTargets derives the step targets from the host's interfaces. Own addresses are every local
// IPv4 address, so the console is never confused with this host.
func systemTargets() (bcast, sweep, own []netip.Addr, err error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil, nil, err
	}
	var all []ifaceAddrs
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, nil, nil, err
		}
		ia := ifaceAddrs{flags: ifc.Flags}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(n.IP)
			ones, bits := n.Mask.Size()
			if !ok || bits == 0 {
				continue
			}
			ip = ip.Unmap()
			if ip.Is4() {
				own = append(own, ip)
				ones -= bits - 32 // a 16-byte mask on an IPv4 address
			}
			ia.addrs = append(ia.addrs, netip.PrefixFrom(ip, ones))
		}
		all = append(all, ia)
	}
	var src netip.Addr
	// Dialing UDP sends nothing; it only asks the kernel which source address the default route uses.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		src = c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
		_ = c.Close()
	}
	for _, n := range routeFirst(filterNets(all), src) {
		if b, ok := broadcast(n.pfx); ok {
			bcast = append(bcast, b)
		}
		sweep = append(sweep, hosts(n.self, n.pfx)...)
	}
	if len(sweep) > maxSweep {
		sweep = sweep[:maxSweep]
	}
	return bcast, sweep, own, nil
}
