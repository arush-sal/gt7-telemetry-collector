package protocol

import (
	"fmt"
	"math"
	"time"
)

// Decode turns a decrypted GT7 packet into a GT7DData. It validates length first (so it never
// panics on a short slice), reads each field from its verified byte offset, and applies the
// documented transforms (unit scaling, gear-nibble and flag-bit unpacking, ms->Duration). Derived
// tyre speeds and slip ratios are computed after the direct reads because they depend on other
// decoded fields. It returns a wrapped error if the packet is too short to decode.
func Decode(dec []byte) (GT7DData, error) {
	if err := Validate(dec); err != nil {
		return GT7DData{}, fmt.Errorf("decode: %w", err)
	}

	flags0 := dec[offFlags0]
	flags1 := dec[offFlags1]
	gear := dec[offGear]

	d := GT7DData{
		// Identity & lap
		PackageID:       u32(dec, offPackageID),
		CarID:           i32(dec, offCarID),
		CurrentLap:      i16(dec, offCurrentLap),
		TotalLaps:       i16(dec, offTotalLaps),
		BestLap:         time.Duration(i32(dec, offBestLap)) * time.Millisecond,
		LastLap:         time.Duration(i32(dec, offLastLap)) * time.Millisecond,
		CurrentPosition: i16(dec, offCurrentPosition),
		TotalPositions:  i16(dec, offTotalPositions),
		TimeOnTrack:     time.Duration(i32(dec, offTimeOnTrack)) * time.Millisecond,

		// Powertrain
		RPM:               f32(dec, offRPM),
		RPMRevWarning:     u16(dec, offRPMRevWarning),
		RPMRevLimiter:     u16(dec, offRPMRevLimiter),
		RPMAfterClutch:    f32(dec, offRPMAfterClutch),
		EstimatedTopSpeed: i16(dec, offEstimatedTop),
		CurrentGear:       gear & 0x0F,
		SuggestedGear:     gear >> 4,
		Clutch:            f32(dec, offClutch),
		ClutchEngaged:     f32(dec, offClutchEngaged),
		Boost:             f32(dec, offBoost) - 1,

		// Pedals (raw byte 0-255 -> 0-100)
		Throttle: float32(dec[offThrottle]) / 2.55,
		Brake:    float32(dec[offBrake]) / 2.55,

		// Speed
		CarSpeed: 3.6 * f32(dec, offCarSpeed),

		// Fuel
		FuelCapacity: f32(dec, offFuelCapacity),
		CurrentFuel:  f32(dec, offCurrentFuel),

		// Motion (world space)
		PositionX:        f32(dec, offPositionX),
		PositionY:        f32(dec, offPositionY),
		PositionZ:        f32(dec, offPositionZ),
		VelocityX:        f32(dec, offVelocityX),
		VelocityY:        f32(dec, offVelocityY),
		VelocityZ:        f32(dec, offVelocityZ),
		RotationPitch:    f32(dec, offRotationPitch),
		RotationYaw:      f32(dec, offRotationYaw),
		RotationRoll:     f32(dec, offRotationRoll),
		AngularVelocityX: f32(dec, offAngularVelX),
		AngularVelocityY: f32(dec, offAngularVelY),
		AngularVelocityZ: f32(dec, offAngularVelZ),
		RideHeight:       1000 * f32(dec, offRideHeight),

		// Tyre diameters (needed below for tyre speed) and temps
		TyreDiameterFL: f32(dec, offTyreDiameterFL),
		TyreDiameterFR: f32(dec, offTyreDiameterFR),
		TyreDiameterRL: f32(dec, offTyreDiameterRL),
		TyreDiameterRR: f32(dec, offTyreDiameterRR),
		TyreTempFL:     f32(dec, offTyreTempFL),
		TyreTempFR:     f32(dec, offTyreTempFR),
		TyreTempRL:     f32(dec, offTyreTempRL),
		TyreTempRR:     f32(dec, offTyreTempRR),

		// Suspension
		SuspensionFL: f32(dec, offSuspensionFL),
		SuspensionFR: f32(dec, offSuspensionFR),
		SuspensionRL: f32(dec, offSuspensionRL),
		SuspensionRR: f32(dec, offSuspensionRR),

		// Temps & pressures
		OilTemp:     f32(dec, offOilTemp),
		WaterTemp:   f32(dec, offWaterTemp),
		OilPressure: f32(dec, offOilPressure),

		// Gear ratios
		Gear1: f32(dec, offGear1),
		Gear2: f32(dec, offGear2),
		Gear3: f32(dec, offGear3),
		Gear4: f32(dec, offGear4),
		Gear5: f32(dec, offGear5),
		Gear6: f32(dec, offGear6),
		Gear7: f32(dec, offGear7),
		Gear8: f32(dec, offGear8),

		// Status flags (byte 0x8E)
		InRace:               bit(flags0, 0),
		IsPaused:             bit(flags0, 1),
		IsLoading:            bit(flags0, 2),
		IsInGear:             bit(flags0, 3),
		CarHasTurbo:          bit(flags0, 4),
		IsRevLimiterFlashing: bit(flags0, 5),
		IsHandbrakeEngaged:   bit(flags0, 6),
		IsLightsOn:           bit(flags0, 7),

		// Status flags (byte 0x8F)
		IsLowBeamOn:   bit(flags1, 0),
		IsHighBeamOn:  bit(flags1, 1),
		IsASMEngaged:  bit(flags1, 2),
		IsTCSEngaged:  bit(flags1, 3),
		UnknownFlag12: bit(flags1, 4),
		UnknownFlag13: bit(flags1, 5),
		UnknownFlag14: bit(flags1, 6),
		UnknownFlag15: bit(flags1, 7),
	}

	// Derived tyre speeds: |3.6 * diameter * angular velocity| (km/h). Depends on the diameters
	// read above, so it runs after the struct literal.
	d.TyreSpeedFL = tyreSpeed(f32(dec, offWheelOmegaFL), d.TyreDiameterFL)
	d.TyreSpeedFR = tyreSpeed(f32(dec, offWheelOmegaFR), d.TyreDiameterFR)
	d.TyreSpeedRL = tyreSpeed(f32(dec, offWheelOmegaRL), d.TyreDiameterRL)
	d.TyreSpeedRR = tyreSpeed(f32(dec, offWheelOmegaRR), d.TyreDiameterRR)

	// Slip ratio = tyre speed / car speed, only meaningful while moving (avoids divide-by-zero).
	if d.CarSpeed > 0 {
		d.TyreSlipRatioFL = d.TyreSpeedFL / d.CarSpeed
		d.TyreSlipRatioFR = d.TyreSpeedFR / d.CarSpeed
		d.TyreSlipRatioRL = d.TyreSpeedRL / d.CarSpeed
		d.TyreSlipRatioRR = d.TyreSpeedRR / d.CarSpeed
	}

	return d, nil
}

// tyreSpeed converts a wheel's angular velocity and diameter to a surface speed in km/h. The value
// is taken as an absolute magnitude (direction is not meaningful here).
func tyreSpeed(omega, diameter float32) float32 {
	return float32(math.Abs(float64(3.6 * diameter * omega)))
}

// PeekPackageID reads just the PackageID (little-endian uint32 at offPackageID) from a decrypted
// packet, letting callers de-duplicate before paying for a full Decode. The packet must already be
// Validate-d: PeekPackageID does not bounds-check beyond what u32 enforces.
func PeekPackageID(dec []byte) uint32 {
	return u32(dec, offPackageID)
}
