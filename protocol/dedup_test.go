package protocol

import "testing"

// TestDedup drives one Deduper through a realistic sequence — cold start, forward progress, a
// duplicate, a small out-of-order packet, and a large backward jump — asserting accept/reset for
// each. The cases are ordered and share state on purpose: they model a single packet stream.
func TestDedup(t *testing.T) {
	tests := []struct {
		name       string
		n          uint32
		wantReset  bool
		wantAccept bool
	}{
		{"first-packet", 0, false, true}, {"forward", 10000, false, true}, {"duplicate", 10000, false, false}, {"small-reorder", 9900, false, false}, {"big-backward-jump", 990, true, true},
	}

	dd := new(Deduper)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accept, reset := dd.Accept(tt.n)

			if accept != tt.wantAccept {
				t.Errorf("Dedup() %s error: want Accept %t got %t", tt.name, tt.wantAccept, accept)
			}
			if reset != tt.wantReset {
				t.Errorf("Dedup() %s error: want reset %t got %t", tt.name, tt.wantReset, reset)
			}
		})
	}
}
