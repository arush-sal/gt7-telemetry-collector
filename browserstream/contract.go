package browserstream

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/telemetry"
)

// Protocol is the WebSocket subprotocol and the value of every message's protocol field.
const Protocol = "gt7.inputs.v1"

// SourceState is the hub's view of the telemetry source.
type SourceState string

// Source states.
const (
	// StateWaiting means no frame has been accepted yet.
	StateWaiting SourceState = "waiting_for_source"
	// StateLive means frames are arriving.
	StateLive SourceState = "source_live"
	// StateStale means no frame arrived within the source timeout.
	StateStale SourceState = "source_stale"
)

// Status reasons and capability reasons.
const (
	// ReasonFirstFrame accompanies the first live status after waiting.
	ReasonFirstFrame = "first_frame"
	// ReasonResumed accompanies the first live status after stale.
	ReasonResumed = "source_resumed"
	// ReasonStaleTimeout accompanies a stale status.
	ReasonStaleTimeout = "no_accepted_frames_within_timeout"
	// ReasonSteeringUnverified explains why steering is unavailable in v1.
	ReasonSteeringUnverified = "driver_steering_input_unverified"
)

// Hello is the first message on every connection.
type Hello struct {
	Type                   string       `json:"type"`
	Protocol               string       `json:"protocol"`
	SessionID              string       `json:"sessionId"`
	CollectorVersion       string       `json:"collectorVersion"`
	CanonicalSchemaVersion int          `json:"canonicalSchemaVersion"`
	Capabilities           Capabilities `json:"capabilities"`
	Status                 SourceState  `json:"status"`
}

// Capabilities advertises which inputs the stream carries.
type Capabilities struct {
	Brake    RangeCapability       `json:"brake"`
	Throttle RangeCapability       `json:"throttle"`
	Steering UnavailableCapability `json:"steering"`
}

// RangeCapability describes an available ranged input.
type RangeCapability struct {
	Available bool   `json:"available"`
	Unit      string `json:"unit"`
	Min       int    `json:"min"`
	Max       int    `json:"max"`
}

// UnavailableCapability describes an input that is not provided.
type UnavailableCapability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// NewHello builds the hello message for a new connection.
func NewHello(sessionID, collectorVersion string, status SourceState) Hello {
	pct := RangeCapability{Available: true, Unit: "percent", Min: 0, Max: 100}
	return Hello{
		Type:                   "hello",
		Protocol:               Protocol,
		SessionID:              sessionID,
		CollectorVersion:       collectorVersion,
		CanonicalSchemaVersion: telemetry.SchemaVersion,
		Capabilities: Capabilities{
			Brake:    pct,
			Throttle: pct,
			Steering: UnavailableCapability{Available: false, Reason: ReasonSteeringUnverified},
		},
		Status: status,
	}
}

// Sample is one projected canonical frame.
type Sample struct {
	Type                 string `json:"type"`
	Protocol             string `json:"protocol"`
	SessionID            string `json:"sessionId"`
	Sequence             string `json:"sequence"`
	PackageID            uint32 `json:"packageId"`
	Source               string `json:"source"`
	RecvTimestamp        string `json:"recvTimestamp"`
	SourceGap            bool   `json:"sourceGap"`
	DroppedSincePrevious uint64 `json:"droppedSincePrevious"`
	Inputs               Inputs `json:"inputs"`
}

// Inputs are the driver inputs. A nil pointer encodes as JSON null ("no reading").
type Inputs struct {
	Brake    *float32         `json:"brake"`
	Throttle *float32         `json:"throttle"`
	Steering unavailableValue `json:"steering"`
}

// unavailableValue always encodes as null, so steering can never carry a number.
type unavailableValue struct{}

// MarshalJSON always returns null.
func (unavailableValue) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// UnmarshalJSON accepts only null, so a steering number can never be decoded either.
func (*unavailableValue) UnmarshalJSON(b []byte) error {
	if string(b) != "null" {
		return errors.New("browserstream: steering must be null in gt7.inputs.v1")
	}
	return nil
}

// Project maps a canonical frame to a Sample. It reads only metadata, Brake and Throttle. Brake or
// Throttle that is non-finite or outside [0,100] becomes nil (never clamped) and counts in invalid.
//
// It returns an error only for a nil frame or a missing/invalid RecvTimestamp. On error the caller
// should drop the frame and count it; no Sample is produced.
func Project(sessionID string, f *telemetryv1.TelemetryFrame, droppedSincePrevious uint64) (s Sample, invalid int, err error) {
	if f == nil || f.RecvTimestamp == nil || !f.RecvTimestamp.IsValid() {
		return Sample{}, 0, errors.New("browserstream: frame is nil or has no valid recv timestamp")
	}
	var src string
	switch f.Source {
	case telemetryv1.Source_SOURCE_LIVE:
		src = "LIVE"
	case telemetryv1.Source_SOURCE_REPLAY:
		src = "REPLAY"
	default:
		src = "UNSPECIFIED"
	}
	b, bad := percent(f.Brake)
	invalid += bad
	t, bad := percent(f.Throttle)
	invalid += bad
	return Sample{
		Type:                 "sample",
		Protocol:             Protocol,
		SessionID:            sessionID,
		Sequence:             strconv.FormatUint(f.Sequence, 10),
		PackageID:            f.PackageId,
		Source:               src,
		RecvTimestamp:        f.RecvTimestamp.AsTime().UTC().Format(time.RFC3339Nano),
		SourceGap:            f.Gap,
		DroppedSincePrevious: droppedSincePrevious,
		Inputs:               Inputs{Brake: b, Throttle: t},
	}, invalid, nil
}

// percent returns v unchanged when finite and within [0,100]. Negative zero is normalised to 0
// because encoding/json would emit "-0"; it is the same value, only the sign bit differs.
func percent(v float32) (*float32, int) {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < 0 || v > 100 {
		return nil, 1
	}
	if v == 0 {
		v = math.Float32frombits(0) // +0 regardless of the sign bit
	}
	return &v, 0
}

// Status is a per-connection source state change.
type Status struct {
	Type      string      `json:"type"`
	Protocol  string      `json:"protocol"`
	SessionID string      `json:"sessionId"`
	State     SourceState `json:"state"`
	Reason    string      `json:"reason"`
}

// NewStatus builds a status message.
func NewStatus(sessionID string, state SourceState, reason string) Status {
	return Status{Type: "status", Protocol: Protocol, SessionID: sessionID, State: state, Reason: reason}
}

// NewSessionID returns 128 random bits as 32 lowercase hex characters.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// DroppedSince returns how many sequences were skipped between prev and cur; 0 when there is no
// previous sample or the sequence did not advance (no underflow).
func DroppedSince(prev, cur uint64, havePrev bool) uint64 {
	if !havePrev || cur <= prev {
		return 0
	}
	return cur - prev - 1
}
