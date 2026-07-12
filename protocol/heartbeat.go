package protocol

// HeartbeatPayload returns the byte the collector sends to the console to keep telemetry flowing.
// The byte selects which packet format the console streams back; we send "C", the largest format —
// a superset of A/B/~ — since our decoder only reads the shared 0x128-byte region common to all
// formats, so requesting the biggest packet costs nothing now and avoids a heartbeat change later
// if a consumer needs C's extra fields:
// 'A' (0x41) → the standard telemetry packet (~296 B, decode bound 0x128).
// 'B' (0x42) → an extended packet (larger, ~316 B) with extra fields past 0x128.
// '~' (0x7E) → the bigger variant (newer GT7 versions — even more fields, e.g. wheel rotation, sway/heave/surge, EV energy recovery).
// 'C' (0x43) → newer extended packet (largest, ~368 B) with extra fields such as surfaceType, wheelSteeringAngle, wheelBase, and carCategory — the format we request.
func HeartbeatPayload() []byte { return []byte("C") }
