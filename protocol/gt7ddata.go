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

	// Format-C tail (0x128..0x170). Only populated when Decode is given a full-length format-C
	// packet (368 bytes); for format-A packets (296 bytes) these all stay at their zero value —
	// see Decode's format-length guard. Confidence/provenance for each field's offset lives as a
	// comment on the matching offXxx constant in constants.go; it is not repeated here.
	WheelRotation           float32 // rad
	SteeringAngularVelocity float32 // rad/s
	Sway                    float32 // X-axis acceleration
	Heave                   float32 // Y-axis acceleration
	Surge                   float32 // Z-axis acceleration

	ThrottleFiltered uint8 // 0-255
	BrakeFiltered    uint8 // 0-255; UNCONFIRMED, see offBrakeFiltered
	UnknownU8_13E    uint8 // UNKNOWN, see offUnknownU8_13E
	UnknownU8_13F    uint8 // UNKNOWN, see offUnknownU8_13F

	// TorqueVectors: per-wheel drive/brake torque (FL,FR,RL,RR), +drive/-brake·regen. UNCONFIRMED,
	// see offTorqueVector{FL,FR,RL,RR}.
	TorqueVectors [4]float32

	EnergyRecovery float32 // battery regen; UNCONFIRMED, see offEnergyRecovery
	UnknownF32_154 float32 // UNKNOWN/under-research, see offUnknownF32_154

	// SurfaceType: one raw material code byte per wheel (FL,FR,RL,RR). Kept RAW/faithful here —
	// Decode does not trim or stringify it; interpreting the code table belongs to a later
	// presentation layer. See offSurfaceType.
	SurfaceType [4]byte

	CurrentLapTimeMs int32 // ms; see offCurrentLapTimeMs

	// WheelSteeringAngle: front-wheel steering angle in rad, {left, right}. See
	// offWheelSteeringAngle{L,R}.
	WheelSteeringAngle [2]float32

	WheelBase float32 // m; see offWheelBase

	// CarCategory: raw null-terminated ASCII category code. Kept RAW/faithful here — Decode does
	// not trim or stringify it; interpretation belongs to a later presentation layer. See
	// offCarCategory.
	CarCategory [4]byte
}
