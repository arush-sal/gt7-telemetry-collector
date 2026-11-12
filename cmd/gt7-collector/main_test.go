package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"missing ps5", []string{"--rtps=localhost:50051", "--pub-buffer=64"}, "--ps5"},
		{"empty rtps", []string{"--ps5=192.0.2.5", "--rtps= "}, "--rtps"},
		{"valid", []string{"--ps5=192.0.2.5", "--rtps=localhost:50051", "--recv-port=40000", "--send-port=40001", "--heartbeat=2s", "--rawdump-dir=captures", "--raw-buffer=32", "--pub-buffer=64", "--log-level=debug"}, ""},
		{"bad log level", []string{"--ps5=192.0.2.5", "--rtps=localhost:50051", "--pub-buffer=64", "--log-level=bogus"}, "--log-level"},
		{"bad pub buffer", []string{"--ps5=192.0.2.5", "--pub-buffer=0"}, "--pub-buffer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlags(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := config{ps5: "192.0.2.5", rtps: "localhost:50051", recvPort: 40000, sendPort: 40001, heartbeat: 2 * time.Second, rawDumpDir: "captures", rawBuffer: 32, pubBuffer: 64, logLevel: "debug"}
			if got != want {
				t.Fatalf("config = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMainExitCodes(t *testing.T) {
	if got := mainExit([]string{"--help"}); got != 0 {
		t.Errorf("--help exit = %d, want 0", got)
	}
	if got := mainExit(nil); got != 2 {
		t.Errorf("missing --ps5 exit = %d, want 2", got)
	}
}
