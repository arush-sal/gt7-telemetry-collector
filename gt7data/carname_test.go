package gt7data

import "testing"

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
