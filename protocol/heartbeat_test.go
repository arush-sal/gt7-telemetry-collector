package protocol

import "testing"

func TestHeartbeatPayload(t *testing.T) {
	h := string(HeartbeatPayload())

	// "C" selects the largest GT7 packet format (368 B): a superset of A/B/~. We send it so the
	// console streams C-format packets we can capture raw off the wire for offset reverse-engineering.
	if h != "C" {
		t.Fatalf("Wrong Heartbeat received: expected 'C', got %s", h)
	}
}
