package ingest

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestReceiver checks the receive path end to end over loopback: a datagram sent to the bound port
// reaches the handler (copied), the heartbeat byte is emitted, and Run returns cleanly on cancel.
func TestReceiver(t *testing.T) {
	// Socket that stands in for the console, to capture the heartbeat.
	hbConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("hb listen: %v", err)
	}
	defer func() { _ = hbConn.Close() }()
	hbPort := hbConn.LocalAddr().(*net.UDPAddr).Port

	got := make(chan []byte, 4)
	handle := func(b []byte, _ time.Time) { got <- b }

	r, err := NewReceiver(0, hbPort, "127.0.0.1", 25*time.Millisecond, handle)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Send a datagram to the receiver.
	raddr := r.LocalAddr().(*net.UDPAddr)
	sender, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: raddr.Port})
	if err != nil {
		t.Fatalf("dial receiver: %v", err)
	}
	defer func() { _ = sender.Close() }()
	want := []byte{0x01, 0x02, 0x03}
	if _, err := sender.Write(want); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Handler should receive a copy of the payload.
	select {
	case b := <-got:
		if string(b) != string(want) {
			t.Errorf("handler got %x, want %x", b, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not receive the datagram")
	}

	// Heartbeat "A" should arrive at the console socket.
	_ = hbConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	n, _, err := hbConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("heartbeat not received: %v", err)
	}
	if string(buf[:n]) != "A" {
		t.Errorf("heartbeat = %q, want \"A\"", buf[:n])
	}

	// Shutdown: cancel must unblock ReadFromUDP and Run must return promptly.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
