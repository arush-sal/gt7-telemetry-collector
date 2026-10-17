package replay

import (
	"math"
	"testing"
	"time"
)

func TestPace(t *testing.T) {
	const ms = int64(time.Millisecond)
	tests := []struct {
		name      string
		prev, cur int64
		speed     float64
		want      time.Duration
	}{
		{"normal 16ms at 1x", 0, 16 * ms, 1.0, 16 * time.Millisecond},
		{"2x speed halves the wait", 0, 16 * ms, 2.0, 8 * time.Millisecond},
		{"0.5x speed doubles the wait", 0, 16 * ms, 0.5, 32 * time.Millisecond},
		{"zero delta", 100 * ms, 100 * ms, 1.0, 0},
		// Out-of-order capture (cur before prev) must not sleep, never a negative duration.
		{"negative delta clamps to zero", 100 * ms, 50 * ms, 1.0, 0},
		// A corrupt timestamp or long dead-air gap must be capped so playback can't freeze.
		{"absurd delta caps at maxPaceSleep", 0, int64(60 * time.Second), 1.0, maxPaceSleep},
		// Non-finite speed should never yield a garbage/undefined duration (defense in depth: input
		// validation rejects these, but pace must be robust regardless). Fast-forward (+Inf/negative/NaN) means no wait.
		{"NaN speed: no wait", 0, 16 * ms, math.NaN(), 0},
		{"+Inf speed: no wait", 0, 16 * ms, math.Inf(1), 0},
		{"negative speed: no wait", 0, 16 * ms, -1, 0},
		{"zero speed caps", 0, 16 * ms, 0, maxPaceSleep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pace(tt.prev, tt.cur, tt.speed); got != tt.want {
				t.Errorf("pace(%d, %d, %v) = %v, want %v", tt.prev, tt.cur, tt.speed, got, tt.want)
			}
		})
	}
}
