package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// wantHeader is the exact header row parseCSV requires. Validating it fails loud if the source
// schema ever drifts, rather than silently mis-parsing.
var wantHeader = []string{"car_id", "car_name"}

// parseCSV reads a carlist.csv stream into car rows. It is strict on purpose: every anomaly
// (unexpected header, bad id, empty or whitespace-padded name, non-UTF-8 bytes, wrong column
// count) is a hard error at generation time, never a silent coercion. Row-name whitespace is not
// trimmed: a padded name is a data error to fix at the source, not to paper over here.
func parseCSV(r io.Reader) ([]carRow, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = 2 // enforce exactly car_id,car_name; csv errors on any other count

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if len(header) != len(wantHeader) || header[0] != wantHeader[0] || header[1] != wantHeader[1] {
		return nil, fmt.Errorf("unexpected header %v, want %v", header, wantHeader)
	}

	var rows []carRow
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read record: %w", err)
		}

		idStr, name := rec[0], rec[1]
		id, err := strconv.ParseInt(idStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("car_id %q: %w", idStr, err)
		}
		if name == "" {
			return nil, fmt.Errorf("car_id %d: empty car_name", id)
		}
		if strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("car_id %d: car_name %q has leading/trailing whitespace", id, name)
		}
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("car_id %d: car_name is not valid UTF-8", id)
		}
		rows = append(rows, carRow{ID: int32(id), Name: name})
	}
	return rows, nil
}

// wantDrivetrainHeader is the exact header row parseDrivetrainCSV requires.
var wantDrivetrainHeader = []string{"car_id", "drivetrain"}

// validDrivetrains is the closed set of drivetrain values the source may carry. It mirrors
// gt7data.IsDrivetrain. It is duplicated rather than imported because of a bootstrap problem: the
// generator writes the gt7data file that defines IsDrivetrain, so it must build before (and without)
// that file existing.
var validDrivetrains = map[string]bool{"FF": true, "FR": true, "MR": true, "RR": true, "4WD": true}

// parseDrivetrainCSV reads a drivetrain.csv stream into an id -> drivetrain map. Like parseCSV it
// is strict: wrong header, bad id, a value outside the known set, or a duplicate id is a hard
// error. An empty value means "unknown" and the row is omitted from the result.
func parseDrivetrainCSV(r io.Reader) (map[int32]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = 2

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if len(header) != 2 || header[0] != wantDrivetrainHeader[0] || header[1] != wantDrivetrainHeader[1] {
		return nil, fmt.Errorf("unexpected header %v, want %v", header, wantDrivetrainHeader)
	}

	out := map[int32]string{}
	seen := map[int32]bool{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read record: %w", err)
		}
		id64, err := strconv.ParseInt(rec[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("car_id %q: %w", rec[0], err)
		}
		id := int32(id64)
		if seen[id] {
			return nil, fmt.Errorf("car_id %d: duplicate drivetrain row", id)
		}
		seen[id] = true
		if rec[1] == "" {
			continue
		}
		if !validDrivetrains[rec[1]] {
			return nil, fmt.Errorf("car_id %d: invalid drivetrain %q", id, rec[1])
		}
		out[id] = rec[1]
	}
	return out, nil
}
