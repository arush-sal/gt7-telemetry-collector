package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/replay"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestParseFlags(t *testing.T) {
	t.Setenv("GARAGE_INGEST_TOKEN", "")
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

func TestRunCreatesPCAP(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := config{ps5: "127.0.0.1", rtps: "localhost:50051", rawDumpDir: dir, recvPort: 0, sendPort: 33739, rawBuffer: 8, pubBuffer: 8, heartbeat: time.Second}
	if err := run(ctx, cfg, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "raw-*.pcap"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("pcap paths: %v, %v", paths, err)
	}
	s, err := replay.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestTokenPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, env, flag, want string
		supplied              bool
	}{
		{name: "none"},
		{name: "environment", env: "env-secret", want: "env-secret"},
		{name: "flag", env: "env-secret", flag: "flag-secret", want: "flag-secret", supplied: true},
		{name: "empty flag falls back", env: "env-secret", want: "env-secret", supplied: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GARAGE_INGEST_TOKEN", tt.env)
			args := []string{"--ps5=127.0.0.1"}
			if tt.supplied {
				args = append(args, "--rtps-token="+tt.flag)
			}
			cfg, err := parseFlags(args)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.rtpsToken != tt.want {
				t.Fatal("incorrect token precedence")
			}
		})
	}
}

func TestPlaintextTokenWarning(t *testing.T) {
	for _, target := range []string{"localhost:50051", "grpc://localhost:50051", "http://localhost/ingest", "https://localhost/ingest", "unix:///tmp/ingest.sock", "unix:/tmp/ingest.sock", "HTTP://localhost/ingest", "GRPC://localhost:50051", "HTTPS://localhost/ingest"} {
		for _, token := range []string{"", "secret-token"} {
			t.Run(target+"/token="+fmt.Sprint(token != ""), func(t *testing.T) {
				core, logs := observer.New(zap.WarnLevel)
				warnPlaintextToken(config{rtps: target, rtpsToken: token}, zap.New(core))
				want := token != "" && !strings.HasPrefix(strings.ToLower(target), "https:") && !strings.HasPrefix(target, "unix:")
				if (logs.Len() == 1) != want {
					t.Fatalf("warning count = %d, want warning %v", logs.Len(), want)
				}
				if strings.Contains(fmt.Sprint(logs.All()), "secret-token") {
					t.Fatal("token leaked in logs")
				}
			})
		}
	}
}

func TestTokenNotInParseError(t *testing.T) {
	const token = "flag-secret"
	_, err := parseFlags([]string{"--ps5=127.0.0.1", "--rtps-token=" + token, token})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("token leaked in flag error")
	}
}

func TestTokenPreservesHelp(t *testing.T) {
	_, err := parseFlags([]string{"--rtps-token=flag-secret", "--help"})
	if !errors.Is(err, pflag.ErrHelp) {
		t.Fatalf("help error = %v, want pflag.ErrHelp", err)
	}
}

func TestTokenRedactionBeforeParsing(t *testing.T) {
	for _, tt := range []struct {
		env  string
		args []string
	}{
		{"secret", []string{"--heartbeat=secret"}},
		{"", []string{"--heartbeat=secret", "--rtps-token=secret"}},
		{"", []string{"--heartbeat=secret", "--rtps-token", "secret"}},
		{"env-secret", []string{"--heartbeat=env-secret", "--rtps-token=flag-secret"}},
		{"env-secret", []string{"--heartbeat=flag-secret", "--rtps-token=flag-secret"}},
		{"", []string{"--heartbeat=secret", "--rtps-token=secret", "--rtps-token=other"}},
	} {
		t.Setenv("GARAGE_INGEST_TOKEN", tt.env)
		_, err := parseFlags(tt.args)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe parse error: %v", err)
		}
	}
}
