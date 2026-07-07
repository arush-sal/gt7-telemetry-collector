// Package gt7data holds GT7-specific reference data that the collector resolves into the
// game-agnostic canonical TelemetryFrame. It is collector-owned and deliberately GT7-only: the
// RTPS must never import it, so that GT7 knowledge stops at the collector's ToCanonical seam.
package gt7data

//go:generate go run ./internal/gen

// CarName returns the display name for a GT7 car model id and whether the id is known. Unknown ids
// return ("", false); the caller decides how to present a miss (the collector maps it to
// "Unknown"). The parameter is int32 to match the canonical frame's car_id field, avoiding
// conversions at call sites.
func CarName(id int32) (string, bool) {
	name, ok := carNames[id]
	return name, ok
}
