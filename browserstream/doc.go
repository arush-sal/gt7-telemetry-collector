// Package browserstream is the read-only WebSocket browser stream of the
// collector. Phase 1 contains only the gt7.inputs.v1 contract: the JSON
// message types, the projection from the canonical TelemetryFrame, and
// session/loss helpers. It has no transport or hub code yet.
package browserstream
