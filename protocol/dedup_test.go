package protocol

import "testing"

// TestAccept drives one Deduper through a realistic sequence — cold start, forward progress, a
// duplicate, a small out-of-order packet, and a large backward jump — asserting accept/reset for
// each. The cases are ordered and share state on purpose: they model a single packet stream.
func TestAccept(t *testing.T) {
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

func TestGap(t *testing.T) {
	dd := &Deduper{}

	// Nothing accepted yet: there is no previous id, so no gap regardless of the value.
	if dd.Gap(10) {
		t.Error("Gap on a fresh Deduper should be false")
	}

	// Establish state: last accepted PackageID is 10. Gap is read-only, so last stays 10 below.
	dd.Accept(10)

	tests := []struct {
		name string
		n    uint32
		want bool
	}{
		{"contiguous", 11, false},            // 11 == last+1, no hole
		{"gap", 15, true},                    // skipped 11..14
		{"duplicate-or-backward", 10, false}, // not forward progress
	}
	for _, tt := range tests {
		if got := dd.Gap(tt.n); got != tt.want {
			t.Errorf("%s: Gap(%d) = %t, want %t", tt.name, tt.n, got, tt.want)
		}
	}
}
