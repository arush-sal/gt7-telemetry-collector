package telemetry

import (
	"time"

	"github.com/arush-sal/gt7/backend/collector/protocol"
	telemetryv1 "github.com/arush-sal/gt7/backend/gen/gt7/telemetry/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SchemaVersion is the version of the canonical TelemetryFrame schema this collector produces. It
// is stamped into every frame so the RTPS knows which schema shape it received. It is a constant,
// not caller-supplied: the collector decides what it emits.
const SchemaVersion = 1

// CaptureMeta carries the per-frame capture context the collector knows but the decoded GT7 packet
// does not: when the packet was received, its position in the stream, whether a gap preceded it,
// and whether it came from a live console or a replay.
type CaptureMeta struct {
	RecvTime time.Time          // wall-clock time the packet was received
	Sequence uint64             // collector-assigned monotonic frame counter
	Gap      bool               // the PackageID was non-contiguous before this frame
	Source   telemetryv1.Source // LIVE or REPLAY
}

// ToCanonical maps a decoded GT7DData plus its capture metadata into a canonical TelemetryFrame.
// This is the seam between GT7-specific and game-agnostic types: everything downstream depends only
// on the returned frame, never on GT7DData. Narrow integers widen to proto's int32/uint32, and lap
// times are left unset (nil) when GT7 reports no time (a negative sentinel).
func ToCanonical(d protocol.GT7DData, m CaptureMeta) *telemetryv1.TelemetryFrame {
	return &telemetryv1.TelemetryFrame{
		// Capture metadata
		RecvTimestamp: timestamppb.New(m.RecvTime),
		Sequence:      m.Sequence,
		Gap:           m.Gap,
		Source:        m.Source,
		SchemaVersion: SchemaVersion,

		// Identity & lap
		PackageId:       d.PackageID,
		CarId:           d.CarID,
		CurrentLap:      int32(d.CurrentLap),
		TotalLaps:       int32(d.TotalLaps),
		BestLap:         optDuration(d.BestLap),
		LastLap:         optDuration(d.LastLap),
		CurrentPosition: int32(d.CurrentPosition),
		TotalPositions:  int32(d.TotalPositions),
		TimeOnTrack:     durationpb.New(d.TimeOnTrack),

		// Powertrain
		Rpm:               d.RPM,
		RpmRevWarning:     uint32(d.RPMRevWarning),
		RpmRevLimiter:     uint32(d.RPMRevLimiter),
		RpmAfterClutch:    d.RPMAfterClutch,
		EstimatedTopSpeed: int32(d.EstimatedTopSpeed),
		CurrentGear:       uint32(d.CurrentGear),
		SuggestedGear:     uint32(d.SuggestedGear),
		Clutch:            d.Clutch,
		ClutchEngaged:     d.ClutchEngaged,
		Boost:             d.Boost,

		// Pedals & speed
		Throttle: d.Throttle,
		Brake:    d.Brake,
		CarSpeed: d.CarSpeed,

		// Fuel
		FuelCapacity: d.FuelCapacity,
		CurrentFuel:  d.CurrentFuel,

		// Motion
		PositionX:        d.PositionX,
		PositionY:        d.PositionY,
		PositionZ:        d.PositionZ,
		VelocityX:        d.VelocityX,
		VelocityY:        d.VelocityY,
		VelocityZ:        d.VelocityZ,
		RotationPitch:    d.RotationPitch,
		RotationYaw:      d.RotationYaw,
		RotationRoll:     d.RotationRoll,
		AngularVelocityX: d.AngularVelocityX,
		AngularVelocityY: d.AngularVelocityY,
		AngularVelocityZ: d.AngularVelocityZ,
		RideHeight:       d.RideHeight,

		// Tyres
		TyreDiameterFl:  d.TyreDiameterFL,
		TyreDiameterFr:  d.TyreDiameterFR,
		TyreDiameterRl:  d.TyreDiameterRL,
		TyreDiameterRr:  d.TyreDiameterRR,
		TyreSpeedFl:     d.TyreSpeedFL,
		TyreSpeedFr:     d.TyreSpeedFR,
		TyreSpeedRl:     d.TyreSpeedRL,
		TyreSpeedRr:     d.TyreSpeedRR,
		TyreSlipRatioFl: d.TyreSlipRatioFL,
		TyreSlipRatioFr: d.TyreSlipRatioFR,
		TyreSlipRatioRl: d.TyreSlipRatioRL,
		TyreSlipRatioRr: d.TyreSlipRatioRR,
		TyreTempFl:      d.TyreTempFL,
		TyreTempFr:      d.TyreTempFR,
		TyreTempRl:      d.TyreTempRL,
		TyreTempRr:      d.TyreTempRR,
		SuspensionFl:    d.SuspensionFL,
		SuspensionFr:    d.SuspensionFR,
		SuspensionRl:    d.SuspensionRL,
		SuspensionRr:    d.SuspensionRR,

		// Temps & pressures
		OilTemp:     d.OilTemp,
		WaterTemp:   d.WaterTemp,
		OilPressure: d.OilPressure,

		// Gear ratios
		Gear1: d.Gear1,
		Gear2: d.Gear2,
		Gear3: d.Gear3,
		Gear4: d.Gear4,
		Gear5: d.Gear5,
		Gear6: d.Gear6,
		Gear7: d.Gear7,
		Gear8: d.Gear8,

		// Status flags
		InRace:               d.InRace,
		IsPaused:             d.IsPaused,
		IsLoading:            d.IsLoading,
		IsInGear:             d.IsInGear,
		CarHasTurbo:          d.CarHasTurbo,
		IsRevLimiterFlashing: d.IsRevLimiterFlashing,
		IsHandbrakeEngaged:   d.IsHandbrakeEngaged,
		IsLightsOn:           d.IsLightsOn,
		IsLowBeamOn:          d.IsLowBeamOn,
		IsHighBeamOn:         d.IsHighBeamOn,
		IsAsmEngaged:         d.IsASMEngaged,
		IsTcsEngaged:         d.IsTCSEngaged,
	}
}

// optDuration returns a protobuf Duration for a lap/track time, or nil when GT7 reported no time
// (a negative value). Leaving the field nil keeps GT7's -1 "unset" sentinel off the wire.
func optDuration(d time.Duration) *durationpb.Duration {
	if d < 0 {
		return nil
	}
	return durationpb.New(d)
}
