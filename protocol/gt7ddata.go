package protocol

import "time"

// GT7DData (Gran Turismo 7 Decoded Data) is the fully-decoded state of one GT7 telemetry packet
// A flat, typed view of the ~90 values the game emits at 60 Hz.
// It is the GT7-specific representation; the collector maps it to the game-agnostic canonical schema before publishing.
type GT7DData struct {
	// Identity & lap
	PackageID       uint32        // monotonic packet counter (0x70); dedup key
	CarID           int32         // in-game car model id
	CurrentLap      int16         // current lap number
	TotalLaps       int16         // total laps in the event
	BestLap         time.Duration // best lap time
	LastLap         time.Duration // last completed lap time
	CurrentPosition int16         // race position
	TotalPositions  int16         // number of cars in the event
	TimeOnTrack     time.Duration // elapsed time on track

	// Powertrain
	RPM               float32
	RPMRevWarning     uint16 // rpm where the shift-warning lights begin
	RPMRevLimiter     uint16 // rpm at the limiter
	RPMAfterClutch    float32
	EstimatedTopSpeed int16
	CurrentGear       uint8 // low nibble of 0x90
	SuggestedGear     uint8 // high nibble of 0x90
	Clutch            float32
	ClutchEngaged     float32
	Boost             float32 // raw - 1

	// Pedals (0-100)
	Throttle float32 // raw / 2.55
	Brake    float32 // raw / 2.55

	// Speed
	CarSpeed float32 // km/h (3.6 * m/s)

	// Fuel
	FuelCapacity float32
	CurrentFuel  float32

	// Motion (world space)
	PositionX, PositionY, PositionZ                      float32
	VelocityX, VelocityY, VelocityZ                      float32
	RotationPitch, RotationYaw, RotationRoll             float32
	AngularVelocityX, AngularVelocityY, AngularVelocityZ float32
	RideHeight                                           float32 // mm (1000 * m)

	// Tyres — front-left, front-right, rear-left, rear-right
	TyreDiameterFL, TyreDiameterFR, TyreDiameterRL, TyreDiameterRR     float32
	TyreSpeedFL, TyreSpeedFR, TyreSpeedRL, TyreSpeedRR                 float32 // km/h
	TyreSlipRatioFL, TyreSlipRatioFR, TyreSlipRatioRL, TyreSlipRatioRR float32 // tyreSpeed/carSpeed
	TyreTempFL, TyreTempFR, TyreTempRL, TyreTempRR                     float32
	SuspensionFL, SuspensionFR, SuspensionRL, SuspensionRR             float32

	// Temps & pressures
	OilTemp     float32
	WaterTemp   float32
	OilPressure float32

	// Gear ratios
	Gear1, Gear2, Gear3, Gear4, Gear5, Gear6, Gear7, Gear8 float32

	// Status flags — byte 0x8E (bits 0-7)
	InRace               bool
	IsPaused             bool
	IsLoading            bool
	IsInGear             bool
	CarHasTurbo          bool
	IsRevLimiterFlashing bool
	IsHandbrakeEngaged   bool
	IsLightsOn           bool
	// Status flags — byte 0x8F (bits 0-3)
	IsLowBeamOn  bool
	IsHighBeamOn bool
	IsASMEngaged bool
	IsTCSEngaged bool
	// bits 12-15: meaning unknown (reverse-engineering gap; kept for completeness)
	UnknownFlag12, UnknownFlag13, UnknownFlag14, UnknownFlag15 bool
}
