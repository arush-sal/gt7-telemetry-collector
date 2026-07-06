package main

import (
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

// TestNewLogger_AtomicLevelApplies checks that the AtomicLevel returned alongside the logger
// actually controls the logger's effective level: a hot-reload calling SetLevel must take effect
// without rebuilding the logger.
func TestNewLogger_AtomicLevelApplies(t *testing.T) {
	log, atomicLevel, err := newLogger("info")
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	if atomicLevel.Level() != zapcore.InfoLevel {
		t.Fatalf("initial level = %v, want info", atomicLevel.Level())
	}
	if !log.Core().Enabled(zapcore.InfoLevel) {
		t.Error("logger should be enabled at info before reload")
	}
	if log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("logger should not be enabled at debug before reload")
	}

	atomicLevel.SetLevel(zapcore.DebugLevel)

	if atomicLevel.Level() != zapcore.DebugLevel {
		t.Fatalf("level after SetLevel = %v, want debug", atomicLevel.Level())
	}
	if !log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("logger should be enabled at debug after SetLevel(debug) — hot-reload did not take effect")
	}
}

// TestNewLogger_InvalidLevel checks the error path returns a zero AtomicLevel and nil logger.
func TestNewLogger_InvalidLevel(t *testing.T) {
	log, _, err := newLogger("trace")
	if err == nil {
		t.Fatal("newLogger(\"trace\"): want error, got nil")
	}
	if log != nil {
		t.Error("newLogger error path should return a nil logger")
	}
}

// TestSendLatestReconfigure_LatestWins checks that two back-to-back sends into an undrained,
// buffered-1 reconfigure channel deliver the LATEST value, not the stale first one. A plain
// non-blocking send with a "channel full" drop (the pre-fix behavior) would leave the first, now
// stale, interval in the channel — exactly backwards from what an operator reloading config expects.
func TestSendLatestReconfigure_LatestWins(t *testing.T) {
	reconfigure := make(chan time.Duration, 1)

	sendLatestReconfigure(reconfigure, 1*time.Second)
	sendLatestReconfigure(reconfigure, 5*time.Second)

	select {
	case got := <-reconfigure:
		if got != 5*time.Second {
			t.Fatalf("delivered = %v, want 5s (latest); stale value won", got)
		}
	default:
		t.Fatal("reconfigure channel unexpectedly empty")
	}

	// The channel must be left empty (not still holding a second, undelivered value).
	select {
	case extra := <-reconfigure:
		t.Fatalf("unexpected extra value in channel: %v", extra)
	default:
	}
}

// TestSendLatestReconfigure_DrainedChannel checks the common case (receiver already drained the
// channel) still delivers correctly with no eviction needed.
func TestSendLatestReconfigure_DrainedChannel(t *testing.T) {
	reconfigure := make(chan time.Duration, 1)

	sendLatestReconfigure(reconfigure, 2*time.Second)
	if got := <-reconfigure; got != 2*time.Second {
		t.Fatalf("delivered = %v, want 2s", got)
	}
}
