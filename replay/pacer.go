package replay

import (
	"math"
	"time"
)

// maxPaceSleep caps the per-frame realtime sleep. pcap capture timestamps are not guaranteed strictly
// monotonic (merged/multi-interface captures), and a capture can contain long dead-air gaps (menu /
// loading between sessions). Capping guards against a corrupt/out-of-order timestamp or a multi-minute
// idle gap freezing playback. It only affects the pacing of dead air (in_race=false frames the session
// detector ignores) — never frame contents and never analysis.
const maxPaceSleep = 2 * time.Second

// pace returns how long to wait before emitting the frame captured at curTS, given the previous
// frame's capture timestamp prevTS (both unix nanoseconds) and a speed multiplier (>1 = faster). A
// non-positive delta (out-of-order capture) yields no wait, and the result is capped at maxPaceSleep.
func pace(prevTS, curTS int64, speed float64) time.Duration {
	d := curTS - prevTS
	if d <= 0 {
		return 0
	}
	// Compute and bound in float space so a non-finite speed can never produce an undefined int64
	// conversion. A non-positive or NaN result (speed +Inf, negative, NaN) means "no wait"; an
	// oversized gap (or speed ~0, giving +Inf) is capped at maxPaceSleep. Input validation also rejects
	// bad speeds, so this is defense in depth.
	scaled := float64(d) / speed
	if math.IsNaN(scaled) || scaled <= 0 {
		return 0
	}
	if scaled >= float64(maxPaceSleep) {
		return maxPaceSleep
	}
	return time.Duration(scaled)
}
