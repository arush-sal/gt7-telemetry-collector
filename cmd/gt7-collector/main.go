// Command gt7-collector receives GT7 UDP telemetry and streams canonical frames to RTPS.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/rawsink"
	"github.com/arush-sal/gt7-telemetry-collector/transport"
	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type config struct {
	ps5, rtps, rawDumpDir, logLevel          string
	recvPort, sendPort, rawBuffer, pubBuffer int
	heartbeat                                time.Duration
}

// parseFlags validates configuration before any resources are opened.
func parseFlags(args []string) (config, error) {
	var c config
	fs := pflag.NewFlagSet("gt7-collector", pflag.ContinueOnError)
	fs.StringVar(&c.ps5, "ps5", "", "PS5 IP address (required)")
	// Defaults mirror backend/internal/config/config.go (LoadBackend); --rtps and --pub-buffer use the
	// removed cmd/collector's rtps.addr and live_buffer.size defaults (backend 91a6668^).
	fs.StringVar(&c.rtps, "rtps", "localhost:50051", "RTPS gRPC ingest address")
	fs.IntVar(&c.recvPort, "recv-port", 33740, "UDP receive port")
	fs.IntVar(&c.sendPort, "send-port", 33739, "UDP heartbeat destination port")
	fs.DurationVar(&c.heartbeat, "heartbeat", time.Second, "heartbeat interval")
	fs.StringVar(&c.rawDumpDir, "rawdump-dir", "", "raw dump directory (empty disables dumping)")
	fs.IntVar(&c.rawBuffer, "raw-buffer", 1024, "raw packet buffer depth")
	fs.IntVar(&c.pubBuffer, "pub-buffer", 1024, "outbound canonical-frame buffer depth")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if c.ps5 == "" {
		return config{}, errors.New("--ps5 is required")
	}
	if net.ParseIP(c.ps5) == nil {
		return config{}, fmt.Errorf("--ps5 must be an IP address: %q", c.ps5)
	}
	if strings.TrimSpace(c.rtps) == "" {
		return config{}, errors.New("--rtps must not be empty")
	}
	if c.recvPort < 1 || c.recvPort > 65535 || c.sendPort < 1 || c.sendPort > 65535 {
		return config{}, errors.New("--recv-port and --send-port must be 1..65535")
	}
	if c.heartbeat <= 0 {
		return config{}, errors.New("--heartbeat must be positive")
	}
	if c.rawBuffer <= 0 {
		return config{}, errors.New("--raw-buffer must be positive")
	}
	if c.pubBuffer <= 0 {
		return config{}, errors.New("--pub-buffer must be positive")
	}
	switch c.logLevel {
	case "debug", "info", "warn", "error":
	default:
		return config{}, fmt.Errorf("invalid --log-level %q: use debug|info|warn|error", c.logLevel)
	}
	return c, nil
}

func main() { os.Exit(mainExit(os.Args[1:])) }

// mainExit keeps cleanup inside the scope that returns the process exit code.
func mainExit(args []string) int {
	cfg, err := parseFlags(args)
	if errors.Is(err, pflag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 2
	}
	log, err := newLogger(cfg.logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger:", err)
		return 2
	}
	defer func() { _ = log.Sync() }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("collector failed", zap.Error(err))
		return 1
	}
	return 0
}

// newLogger builds a production (JSON) zap logger at the given level.
func newLogger(level string) (*zap.Logger, error) {
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	c := zap.NewProductionConfig()
	c.Level = zap.NewAtomicLevelAt(l)
	return c.Build()
}

// run binds before starting workers, then stops receive, publish, and raw output in order.
func run(ctx context.Context, cfg config, log *zap.Logger) error {
	var raw rawsink.RawSink
	if cfg.rawDumpDir != "" {
		path := filepath.Join(cfg.rawDumpDir, fmt.Sprintf("raw-%d.bin", time.Now().Unix()))
		sink, err := rawsink.NewLocalSink(afero.NewOsFs(), path, cfg.rawBuffer)
		if err != nil {
			return fmt.Errorf("raw sink: %w", err)
		}
		sink.SetLogger(log)
		raw = sink
		log.Info("raw dump enabled", zap.String("path", path))
	}
	pub := transport.NewPublisher(cfg.rtps, cfg.pubBuffer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	pub.SetLogger(log)
	pipeline := ingest.NewPipeline(raw, pub)
	recv, err := ingest.NewReceiver(cfg.recvPort, cfg.sendPort, cfg.ps5, cfg.heartbeat, pipeline.Handle)
	if err != nil {
		if raw != nil {
			if closeErr := raw.Close(); closeErr != nil {
				log.Warn("raw sink close", zap.Error(closeErr))
			}
		}
		return err
	}
	recv.SetLogger(log)
	log.Info("collector starting", zap.String("ps5", cfg.ps5), zap.String("rtps", cfg.rtps),
		zap.Stringer("recv_addr", recv.LocalAddr()), zap.Int("send_port", cfg.sendPort))

	// Only the receiver sees the signal context. Its return cancels the publisher,
	// ensuring no producer remains active when publishing stops.
	pubCtx, cancelPub := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelPub()
	var g errgroup.Group
	g.Go(func() error {
		defer cancelPub()
		if err := recv.Run(ctx); !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		if err := pub.Run(pubCtx); !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	g.Go(func() error { report(pubCtx, log, pipeline, pub, raw); return nil })
	runErr := g.Wait()
	if raw != nil {
		if err := raw.Close(); err != nil {
			log.Warn("raw sink close", zap.Error(err))
		}
	}
	logCounters(log, "collector stopped", pipeline, pub, raw)
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// logCounters reports pipeline accounting and both bounded-buffer drop counts.
func logCounters(log *zap.Logger, msg string, p *ingest.Pipeline, pub *transport.Publisher, raw rawsink.RawSink) {
	c := p.Counters()
	var rawDropped uint64
	if sink, ok := raw.(*rawsink.LocalSink); ok {
		rawDropped = sink.Dropped()
	}
	log.Info(msg, zap.Uint64("published", c.Published), zap.Uint64("valid", c.Valid),
		zap.Uint64("foreign", c.Foreign), zap.Uint64("short", c.Short), zap.Uint64("stale", c.Stale),
		zap.Uint64("resets", c.Resets), zap.Uint64("gaps", c.Gaps),
		zap.Uint64("pub_dropped", pub.Dropped()), zap.Uint64("raw_dropped", rawDropped))
}

// report keeps logging off the receive path and emits an INFO snapshot every ten seconds.
func report(ctx context.Context, log *zap.Logger, p *ingest.Pipeline, pub *transport.Publisher, raw rawsink.RawSink) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			logCounters(log, "telemetry stats", p, pub, raw)
		}
	}
}
