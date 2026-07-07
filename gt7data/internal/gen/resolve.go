package main

import (
	"fmt"
	"regexp"
	"strings"
)

// carRow is one parsed carlist.csv record: a GT7 car model id and its raw name.
type carRow struct {
	ID   int32
	Name string
}

// yearSuffix matches a Gran Turismo model-year suffix: a space, an apostrophe, then exactly two
// digits, anchored to the end of the string. It is terminal-anchored on purpose so that an
// apostrophe token elsewhere in the name (e.g. "Mini-Cooper 'S'") is never touched, and a
// four-digit year (e.g. "..., 2017") is not mistaken for the suffix.
var yearSuffix = regexp.MustCompile(` '\d\d$`)

// stripYear removes a trailing " 'NN" model-year suffix from a car name. It returns the name with
// the suffix removed and whether a suffix was present. Names with no trailing year are returned
// unchanged with hadYear=false.
func stripYear(name string) (base string, hadYear bool) {
	if loc := yearSuffix.FindStringIndex(name); loc != nil {
		return name[:loc[0]], true
	}
	return name, false
}

// resolve turns raw car rows into the final id->display-name map. The display rule: a car whose
// year-stripped base name is unique across the list loses its trailing year (" 'NN"); a car that
// shares a base with another keeps its full name so the year disambiguates them. Base-name
// grouping and the final duplicate check are both case-insensitive, since car names are not treated
// as case-sensitive. resolve fails loud (rather than silently shadowing entries) on a duplicate
// car_id or on two rows that would resolve to the same display name.
func resolve(rows []carRow) (map[int32]string, error) {
	// First pass: reject duplicate ids and count how many rows share each base name (case-folded),
	// so the second pass knows whether a given car's base is unique.
	seenID := make(map[int32]bool, len(rows))
	baseCount := make(map[string]int, len(rows))
	for _, r := range rows {
		if seenID[r.ID] {
			return nil, fmt.Errorf("duplicate car_id %d", r.ID)
		}
		seenID[r.ID] = true
		base, _ := stripYear(r.Name)
		baseCount[strings.ToLower(base)]++
	}

	// Second pass: emit the display name and reject any collision in the final output.
	out := make(map[int32]string, len(rows))
	seenDisplay := make(map[string]int32, len(rows))
	for _, r := range rows {
		base, _ := stripYear(r.Name)
		display := r.Name
		if baseCount[strings.ToLower(base)] == 1 {
			display = base // unique base -> drop the year
		}
		key := strings.ToLower(display)
		if prev, ok := seenDisplay[key]; ok {
			return nil, fmt.Errorf("duplicate display name %q for car_id %d and %d", display, prev, r.ID)
		}
		seenDisplay[key] = r.ID
		out[r.ID] = display
	}
	return out, nil
}
