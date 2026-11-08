package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCSV(t *testing.T) {
	in := "car_id,car_name\n24,Nissan 180SX Type X '96\n296,Mazda 787B '91\n"
	want := []carRow{
		{24, "Nissan 180SX Type X '96"},
		{296, "Mazda 787B '91"},
	}
	got, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCSV() = %v, want %v", got, want)
	}
}

func TestParseCSVQuotedFields(t *testing.T) {
	// Row 2123 in the real data: embedded quotes and commas. encoding/csv must handle this; a
	// naive strings.Split would not.
	in := "car_id,car_name\n2123,\"LEXUS LF-LC GT \"\"Vision Gran Turismo\"\"\"\n"
	got, err := parseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseCSV() unexpected error: %v", err)
	}
	want := "LEXUS LF-LC GT \"Vision Gran Turismo\""
	if len(got) != 1 || got[0].Name != want {
		t.Errorf("parseCSV() = %v, want single row name %q", got, want)
	}
}

func TestParseCSVErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"wrong header", "id,name\n24,Foo\n"},
		{"non-numeric id", "car_id,car_name\nxx,Foo\n"},
		{"id out of int32 range", "car_id,car_name\n9999999999,Foo\n"},
		{"empty name", "car_id,car_name\n24,\n"},
		{"whitespace-padded name", "car_id,car_name\n24, Foo \n"},
		{"whitespace-only name", "car_id,car_name\n24,   \n"},
		{"too few columns", "car_id,car_name\n24\n"},
		{"too many columns", "car_id,car_name\n24,Foo,extra\n"},
		{"non-utf8 name", "car_id,car_name\n24,Bad\xffName\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCSV(strings.NewReader(tt.in)); err == nil {
				t.Errorf("parseCSV(%q) = nil error, want error", tt.in)
			}
		})
	}
}

func TestParseDrivetrainCSV(t *testing.T) {
	got, err := parseDrivetrainCSV(strings.NewReader("car_id,drivetrain\n2107,\n3405,MR\n"))
	if err != nil {
		t.Fatalf("parseDrivetrainCSV() unexpected error: %v", err)
	}
	want := map[int32]string{3405: "MR"} // empty value omitted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDrivetrainCSV() = %v, want %v", got, want)
	}
}

func TestParseDrivetrainCSVErrors(t *testing.T) {
	cases := map[string]struct{ in, wantSub string }{
		"bad value":    {"car_id,drivetrain\n77,XX\n", "77"},
		"bad header":   {"id,drivetrain\n77,FF\n", "header"},
		"duplicate id": {"car_id,drivetrain\n77,FF\n77,FR\n", "77"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseDrivetrainCSV(strings.NewReader(c.in))
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("parseDrivetrainCSV() error = %v, want containing %q", err, c.wantSub)
			}
		})
	}
}
