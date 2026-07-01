package protocol

// HeartbeatPayload returns the byte the collector sends to the console to keep telemetry flowing.
// The byte selects which packet format the console streams back; we send "A", the format our
// decoder targets:
// 'A' (0x41) → the standard telemetry packet (~296 B, decode bound 0x128) — exactly what our decoder targets.
// 'B' (0x42) → an extended packet (larger, ~316 B) with extra fields past 0x128.
// '~' (0x7E) → the largest variant (newer GT7 versions — even more fields, e.g. wheel rotation, sway/heave/surge, EV energy recovery).
// 'B'/'~' unlock extended formats deferred until a consumer needs them (and would require their own verified offsets).
func HeartbeatPayload() []byte { return []byte("A") }
