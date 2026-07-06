// Command collector receives GT7 UDP telemetry, decodes it, and streams canonical frames to the
// RTPS over gRPC.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arush-sal/garage/backend/internal/app"
	"github.com/arush-sal/garage/backend/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	log, atomicLevel, err := newLogger(cfg.LogLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger:", err)
		os.Exit(2)
	}
	defer func() { _ = log.Sync() }()

	// Ctrl-C / SIGTERM cancels the context, triggering a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Config hot-reload: only when a --config file was actually given. viper's WatchConfig with no
	// configured file still spawns a watcher goroutine and logs a spurious error, so this guard
	// keeps the flag/env-only path (the common case) free of that.
	reconfigure := make(chan time.Duration, 1)
	if cfg.ConfigPath != "" {
		current := cfg
		stopWatch, err := config.Watch(cfg.ConfigPath, func() {
			next, err := config.Load(os.Args[1:])
			if err != nil {
				log.Warn("config reload failed", zap.Error(err))
				return
			}
			live, restart := config.ClassifyCollectorReload(current, next)
			if d, ok := live["log.level"]; ok {
				var lvl zapcore.Level
				if err := lvl.UnmarshalText([]byte(next.LogLevel)); err != nil {
					log.Warn("config reload: invalid log.level", zap.String("value", next.LogLevel), zap.Error(err))
				} else {
					atomicLevel.SetLevel(lvl)
					log.Info("log level changed", zap.String("old", d[0]), zap.String("new", d[1]))
				}
			}
			if d, ok := live["heartbeat.interval"]; ok {
				sendLatestReconfigure(reconfigure, next.HeartbeatInterval)
				log.Info("heartbeat interval changed", zap.String("old", d[0]), zap.String("new", d[1]))
			}
			for key, d := range restart {
				log.Warn("config change requires restart", zap.String("key", key), zap.String("old", d[0]), zap.String("new", d[1]))
			}
			current = next
		})
		if err != nil {
			log.Warn("config watch failed to start; hot-reload disabled", zap.Error(err))
		} else {
			log.Info("config hot-reload watching "+cfg.ConfigPath+
				" (log level + heartbeat apply live; other changes need restart; "+
				"note: some editor save patterns can stop the watcher)",
				zap.String("path", cfg.ConfigPath))
			defer stopWatch()
		}
	}

	if err := app.Run(ctx, cfg, log, reconfigure); err != nil {
		log.Error("collector failed", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
}

// sendLatestReconfigure delivers next to reconfigure with latest-wins semantics: reconfigure is a
// buffered-1 channel drained by app.Run's forwarding goroutine, and the OnConfigChange callback here
// is the sole producer. If a reload arrives before the previous one is drained, dropping the NEW
// value (as a plain non-blocking send with a "channel full" fallback would) means the stale older
// interval wins — backwards from what an operator expects. Evicting the queued value first, then
// sending, guarantees the newest interval is always what app.Run reads next. Because there is only
// one producer, this loop always terminates (at most one eviction is ever needed).
func sendLatestReconfigure(reconfigure chan time.Duration, next time.Duration) {
	for {
		select {
		case reconfigure <- next:
			return
		default:
			select {
			case <-reconfigure: // evict the stale queued value
			default:
			}
		}
	}
}

// newLogger builds a structured (JSON) zap logger at the given level (debug|info|warn|error). It
// returns the AtomicLevel backing the logger's level so a config hot-reload can change it in place
// (zap.AtomicLevel is safe for concurrent SetLevel/level checks).
func newLogger(level string) (*zap.Logger, zap.AtomicLevel, error) {
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, zap.AtomicLevel{}, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	c := zap.NewProductionConfig()
	atomicLevel := zap.NewAtomicLevelAt(l)
	c.Level = atomicLevel
	logger, err := c.Build()
	if err != nil {
		return nil, zap.AtomicLevel{}, err
	}
	return logger, atomicLevel, nil
}
