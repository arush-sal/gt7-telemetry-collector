package main

import (
	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
)

// fanout delivers each frame to every destination in order. Every destination must itself be
// non-blocking (drop-oldest), so fanout needs no queue of its own. All destinations receive the
// same frame pointer, which is immutable after Publish.
type fanout []ingest.Publisher

// Publish passes f to each destination in turn.
func (fo fanout) Publish(f *telemetryv1.TelemetryFrame) {
	for _, p := range fo {
		p.Publish(f)
	}
}
