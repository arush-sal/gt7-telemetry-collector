package protocol

import "testing"

func TestHeartbeatPayload(t *testing.T) {
	h := string(HeartbeatPayload())

	if h != "A" {
		t.Fatalf("Wrong Heartbeat received: expected 'A', got %s", h)
	}
}
