package gt7data

import (
	"slices"
	"strings"
	"testing"
)

func TestCarNameKnown(t *testing.T) {
	// 104 has a unique base name, so its year is dropped.
	if got, ok := CarName(104); !ok || got != "Nissan Sileighty" {
		t.Errorf("CarName(104) = (%q, %v), want (%q, true)", got, ok, "Nissan Sileighty")
	}
	// 293 shares a base with 396, so the year is kept to disambiguate.
	if got, ok := CarName(293); !ok || got != "Honda NSX Type R '92" {
		t.Errorf("CarName(293) = (%q, %v), want (%q, true)", got, ok, "Honda NSX Type R '92")
	}
}

func TestCarNameUnknown(t *testing.T) {
	for _, id := range []int32{-1, 0, 999999} {
		if got, ok := CarName(id); ok || got != "" {
			t.Errorf("CarName(%d) = (%q, %v), want (\"\", false)", id, got, ok)
		}
	}
}

func TestDrivetrain(t *testing.T) {
	for id, want := range map[int32]string{3405: "MR", 396: "MR"} {
		if got, ok := Drivetrain(id); !ok || got != want {
			t.Errorf("Drivetrain(%d) = (%q, %v), want (%q, true)", id, got, ok, want)
		}
	}
	for _, id := range []int32{2107, 1} { // 2107: empty drivetrain; 1: unknown id
		if got, ok := Drivetrain(id); ok || got != "" {
			t.Errorf("Drivetrain(%d) = (%q, %v), want (\"\", false)", id, got, ok)
		}
	}
	if !slices.Contains(CarIDsWithDrivetrain("MR"), 3405) {
		t.Error("CarIDsWithDrivetrain(MR) missing 3405")
	}
	if CarIDsWithDrivetrain("XX") != nil {
		t.Error("CarIDsWithDrivetrain(XX) != nil")
	}
	if !IsDrivetrain("4WD") || IsDrivetrain("XX") || IsDrivetrain("") {
		t.Error("IsDrivetrain wrong")
	}
	cars := Cars()
	if len(cars) != 570 {
		t.Errorf("len(Cars()) = %d, want 570", len(cars))
	}
	if !slices.IsSortedFunc(cars, func(a, b Car) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("Cars() not sorted by name")
	}
}
