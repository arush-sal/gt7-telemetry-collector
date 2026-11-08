// Package gt7data holds GT7-specific reference data (car names and drivetrains) that the collector
// resolves into the game-agnostic canonical TelemetryFrame. It is deliberately GT7-only. Besides
// the collector, the rtps backend and internal/db may import it for car names and drivetrain,
// resolving on read; the canonical frame and stored data stay game-agnostic.
package gt7data

import (
	"slices"
	"strings"
	"sync"
)

//go:generate go run ./internal/gen

// CarName returns the display name for a GT7 car model id and whether the id is known. Unknown ids
// return ("", false); the caller decides how to present a miss (the collector maps it to
// "Unknown"). The parameter is int32 to match the canonical frame's car_id field, avoiding
// conversions at call sites.
func CarName(id int32) (string, bool) {
	name, ok := carNames[id]
	return name, ok
}

// Drivetrain returns the drivetrain (FF, FR, MR, RR or 4WD) for a GT7 car model id. It returns
// ("", false) for an unknown id or a car whose drivetrain is unknown.
func Drivetrain(id int32) (string, bool) {
	d, ok := carDrivetrains[id]
	return d, ok
}

// IsDrivetrain reports whether d is one of the five drivetrain values: FF, FR, MR, RR, 4WD.
func IsDrivetrain(d string) bool {
	switch d {
	case "FF", "FR", "MR", "RR", "4WD":
		return true
	}
	return false
}

// idsByDrivetrain groups car ids by drivetrain, ascending. Built once because the tables are
// immutable after init.
var idsByDrivetrain = sync.OnceValue(func() map[string][]int32 {
	m := map[string][]int32{}
	for id, d := range carDrivetrains {
		m[d] = append(m[d], id)
	}
	for _, ids := range m {
		slices.Sort(ids)
	}
	return m
})

// CarIDsWithDrivetrain returns the ids of all cars with drivetrain d in ascending order, or nil
// if d is not a known drivetrain value. The returned slice is shared; callers must not modify it.
func CarIDsWithDrivetrain(d string) []int32 {
	return idsByDrivetrain()[d]
}

// Car is one catalogue entry: id, display name, and drivetrain ("" when unknown).
type Car struct {
	ID         int32
	Name       string
	Drivetrain string
}

// Cars returns every known car sorted by display name (for GET /v1/cars). A fresh slice is
// returned on each call.
func Cars() []Car {
	cars := make([]Car, 0, len(carNames))
	for id, name := range carNames {
		cars = append(cars, Car{ID: id, Name: name, Drivetrain: carDrivetrains[id]})
	}
	slices.SortFunc(cars, func(a, b Car) int { return strings.Compare(a.Name, b.Name) })
	return cars
}
