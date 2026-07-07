package main

import (
	"reflect"
	"testing"
)

func TestStripYear(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantBase string
		wantYear bool
	}{
		{"trailing year dropped", "Nissan Sileighty '98", "Nissan Sileighty", true},
		{"no year", "GReddy Fugu Z", "GReddy Fugu Z", false},
		// A four-digit "2017" is NOT the ' NN suffix shape, so it must not be stripped.
		{"four digit year not stripped", "Peugeot L500R HYbrid Vision Gran Turismo, 2017", "Peugeot L500R HYbrid Vision Gran Turismo, 2017", false},
		// The strip is terminal-anchored: an apostrophe-letters token mid-name must survive; only
		// the trailing ' NN goes.
		{"internal apostrophe survives", "Mini-Cooper 'S' '65", "Mini-Cooper 'S'", true},
		// A four-digit number leading the name is not a trailing year.
		{"leading year not stripped", "1932 Ford Roadster", "1932 Ford Roadster", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, hadYear := stripYear(tt.in)
			if base != tt.wantBase || hadYear != tt.wantYear {
				t.Errorf("stripYear(%q) = (%q, %v), want (%q, %v)", tt.in, base, hadYear, tt.wantBase, tt.wantYear)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		rows []carRow
		want map[int32]string
	}{
		{
			name: "unique base drops the year",
			rows: []carRow{{104, "Nissan Sileighty '98"}},
			want: map[int32]string{104: "Nissan Sileighty"},
		},
		{
			name: "shared base keeps years to disambiguate",
			rows: []carRow{{293, "Honda NSX Type R '92"}, {396, "Honda NSX Type R '02"}},
			want: map[int32]string{293: "Honda NSX Type R '92", 396: "Honda NSX Type R '02"},
		},
		{
			name: "collision detection is case-insensitive",
			rows: []carRow{{1, "Foo '98"}, {2, "foo '99"}},
			want: map[int32]string{1: "Foo '98", 2: "foo '99"},
		},
		{
			// A yearless row sharing a base with a yeared row is a collision: both keep their full
			// form, which stays distinct because only one carries a year.
			name: "yearless collides with year-stripped base",
			rows: []carRow{{1, "Foo Bar"}, {2, "Foo Bar '98"}},
			want: map[int32]string{1: "Foo Bar", 2: "Foo Bar '98"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(tt.rows)
			if err != nil {
				t.Fatalf("resolve() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolve() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		rows []carRow
	}{
		{"duplicate car_id", []carRow{{1, "Alpha"}, {1, "Beta"}}},
		// Two distinct ids whose names resolve to the same display string must fail loud, not
		// silently shadow one another in the map.
		{"duplicate display name", []carRow{{1, "Gamma"}, {2, "Gamma"}}},
		{"duplicate display name case-insensitive", []carRow{{1, "Delta"}, {2, "delta"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resolve(tt.rows); err == nil {
				t.Errorf("resolve(%v) = nil error, want error", tt.rows)
			}
		})
	}
}
