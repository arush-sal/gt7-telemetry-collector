// Command gt7-collector receives GT7 UDP telemetry and streams canonical frames to RTPS.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/browserstream"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/rawsink"
	"github.com/arush-sal/gt7-telemetry-collector/transport"
	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
)

type config struct {
	ps5, rtps, rtpsToken, rawDumpDir, logLevel string
	recvPort, sendPort, rawBuffer, pubBuffer   int
	heartbeat                                  time.Duration
	showVersion                                bool
	ws                                         *wsConfig // nil = browser stream disabled
}

// wsConfig is the validated browser stream configuration (ADR 0001, ADR 0003).
type wsConfig struct {
	bindAddr                    string
	loopback                    bool
	origins                     []string
	token                       string
	maxClients, clientBuffer    int
	writeTimeout, sourceTimeout time.Duration
	allowInsecureLAN            bool
	tlsCert, tlsKey             string
}

// Set at release time via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var version, commit, date = "dev", "none", "unknown"

// parseFlags validates configuration before any resources are opened.
func parseFlags(args []string) (_ config, err error) {
	var c config
	envToken := os.Getenv("INGEST_TOKEN")
	wsToken := os.Getenv("GT7_WS_TOKEN")
	defer func() { err = redact(err, secrets(args)...) }()
	fs := pflag.NewFlagSet("gt7-collector", pflag.ContinueOnError)
	fs.StringVar(&c.ps5, "ps5", "", "PS5 IP address (required)")
	fs.StringVar(&c.rtps, "rtps", "localhost:50051", "RTPS target: host:port, grpc://host:port, unix:///path.sock, http(s)://host/path, or none (requires --ws-listen)")
	fs.StringVar(&c.rtpsToken, "rtps-token", "", "ingest bearer token (empty uses INGEST_TOKEN; prefer env to keep it out of ps)")
	fs.IntVar(&c.recvPort, "recv-port", 33740, "UDP receive port")
	fs.IntVar(&c.sendPort, "send-port", 33739, "UDP heartbeat destination port")
	fs.DurationVar(&c.heartbeat, "heartbeat", time.Second, "heartbeat interval")
	fs.StringVar(&c.rawDumpDir, "rawdump-dir", "", "raw dump directory (empty disables dumping)")
	fs.IntVar(&c.rawBuffer, "raw-buffer", 1024, "raw packet buffer depth")
	fs.IntVar(&c.pubBuffer, "pub-buffer", 1024, "outbound canonical-frame buffer depth")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	fs.BoolVar(&c.showVersion, "version", false, "print version information and exit")
	var ws wsConfig
	var wsListen string
	fs.StringVar(&wsListen, "ws-listen", "", "browser WebSocket listen host:port, host an IP literal or localhost (empty disables)")
	fs.StringArrayVar(&ws.origins, "ws-origin", nil, "allowed browser origin scheme://host[:port]; repeatable, required with --ws-listen")
	fs.IntVar(&ws.maxClients, "ws-max-clients", 4, "maximum concurrent WebSocket clients (1..64)")
	fs.IntVar(&ws.clientBuffer, "ws-client-buffer", 64, "per-client sample queue depth (1..4096)")
	fs.DurationVar(&ws.writeTimeout, "ws-write-timeout", 2*time.Second, "per-message write deadline (100ms..60s)")
	fs.DurationVar(&ws.sourceTimeout, "ws-source-timeout", 2*time.Second, "no-frame interval before source_stale (100ms..60s)")
	fs.BoolVar(&ws.allowInsecureLAN, "ws-allow-insecure-lan", false, "permit plaintext ws:// on a non-loopback --ws-listen")
	fs.StringVar(&ws.tlsCert, "ws-tls-cert", "", "TLS certificate file for wss:// (requires --ws-tls-key)")
	fs.StringVar(&ws.tlsKey, "ws-tls-key", "", "TLS private key file for wss:// (requires --ws-tls-cert)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if c.showVersion {
		return c, nil
	}
	if c.rtps == "none" {
		// RTPS is off: a globally set INGEST_TOKEN is ignored, an explicit flag is an error.
		if c.rtpsToken != "" {
			return config{}, errors.New("--rtps-token must not be set with --rtps=none")
		}
		if wsListen == "" {
			return config{}, errors.New("--rtps=none requires --ws-listen")
		}
	} else if c.rtpsToken == "" {
		c.rtpsToken = envToken
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
	if wsListen == "" {
		for _, name := range []string{"ws-origin", "ws-max-clients", "ws-client-buffer", "ws-write-timeout",
			"ws-source-timeout", "ws-allow-insecure-lan", "ws-tls-cert", "ws-tls-key"} {
			if fs.Changed(name) {
				return config{}, fmt.Errorf("--ws-* flags require --ws-listen (got --%s)", name)
			}
		}
		return c, nil
	}
	if err := ws.validate(wsListen, wsToken); err != nil {
		return config{}, err
	}
	c.ws = &ws
	return c, nil
}

// validate checks the browser stream flags (ADR 0003) and fills in the derived fields.
func (w *wsConfig) validate(listen, token string) error {
	var err error
	if w.bindAddr, w.loopback, err = browserstream.ParseListen(listen); err != nil {
		return fmt.Errorf("--ws-listen: %w", err)
	}
	if len(w.origins) == 0 {
		return errors.New("--ws-origin is required with --ws-listen")
	}
	var origins []string
	for _, o := range w.origins {
		n, err := browserstream.NormalizeOrigin(o)
		if err != nil {
			return fmt.Errorf("--ws-origin %q: %w", o, err)
		}
		if !slices.Contains(origins, n) {
			origins = append(origins, n)
		}
	}
	w.origins = origins
	switch {
	case w.maxClients < 1 || w.maxClients > 64:
		return errors.New("--ws-max-clients must be 1..64")
	case w.clientBuffer < 1 || w.clientBuffer > 4096:
		return errors.New("--ws-client-buffer must be 1..4096")
	case w.writeTimeout < 100*time.Millisecond || w.writeTimeout > time.Minute:
		return errors.New("--ws-write-timeout must be 100ms..60s")
	case w.sourceTimeout < 100*time.Millisecond || w.sourceTimeout > time.Minute:
		return errors.New("--ws-source-timeout must be 100ms..60s")
	case (w.tlsCert == "") != (w.tlsKey == ""):
		return errors.New("--ws-tls-cert and --ws-tls-key must be set together")
	}
	if token != "" {
		if err := browserstream.ValidateToken(token); err != nil {
			return fmt.Errorf("GT7_WS_TOKEN: %w", err)
		}
	}
	w.token = token
	if !w.loopback {
		if token == "" {
			return errors.New("GT7_WS_TOKEN is required for a non-loopback --ws-listen")
		}
		if w.tlsCert == "" && !w.allowInsecureLAN {
			return errors.New("a non-loopback --ws-listen needs --ws-tls-cert/--ws-tls-key or --ws-allow-insecure-lan")
		}
	}
	return nil
}

// secrets returns every credential that may appear in an error: INGEST_TOKEN, GT7_WS_TOKEN and
// each --rtps-token value, taken from the raw arguments so even unparsable ones are covered.
func secrets(args []string) []string {
	tokens := []string{os.Getenv("INGEST_TOKEN"), os.Getenv("GT7_WS_TOKEN")}
	for i, arg := range args {
		if arg == "--rtps-token" && i+1 < len(args) {
			tokens = append(tokens, args[i+1])
		} else if strings.HasPrefix(arg, "--rtps-token=") {
			tokens = append(tokens, strings.TrimPrefix(arg, "--rtps-token="))
		}
	}
	return tokens
}

// redact replaces each non-empty token, raw or in its %q-escaped form, with [REDACTED]. An error
// without any token is returned unchanged (so errors.Is still works on it).
func redact(err error, tokens ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, token := range tokens {
		if token == "" {
			continue
		}
		q := strconv.Quote(token)
		message = strings.ReplaceAll(message, q[1:len(q)-1], "[REDACTED]")
		message = strings.ReplaceAll(message, token, "[REDACTED]")
	}
	if message == err.Error() {
		return err
	}
	return errors.New(message)
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
	if cfg.showVersion {
		v := version
		if v == "dev" {
			// go install builds carry no ldflags; fall back to the module version Go embedded.
			if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				v = bi.Main.Version
			}
		}
		fmt.Printf("gt7-collector %s (commit %s, built %s)\n", v, commit, date)
		return 0
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
		log.Error("collector failed", zap.Error(redact(err, secrets(args)...)))
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

// warnPlaintextToken reports when bearer credentials will travel without encryption.
func warnPlaintextToken(cfg config, log *zap.Logger) {
	plaintext, err := transport.Plaintext(cfg.rtps)
	if cfg.rtpsToken != "" && err == nil && plaintext {
		log.Warn("ingest bearer token will be sent unencrypted over a plaintext transport")
	}
}

// run binds before starting workers, then stops receive, browser stream, publish, and raw output
// in that order.
func run(ctx context.Context, cfg config, log *zap.Logger) error {
	var outs fanout
	var pub *transport.Publisher
	if cfg.rtps != "none" {
		p, err := transport.New(cfg.rtps, cfg.pubBuffer, log, transport.WithToken(cfg.rtpsToken))
		if err != nil {
			return fmt.Errorf("publisher: %w", err)
		}
		pub = p
		outs = append(outs, pub)
		warnPlaintextToken(cfg, log)
	}
	var hub *browserstream.Hub
	var srv *browserstream.Server
	if cfg.ws != nil {
		var err error
		if hub, srv, err = newBrowserStream(cfg.ws, log); err != nil {
			return err
		}
		outs = append(outs, hub)
	}
	if len(outs) == 0 {
		return errors.New("no output: --rtps=none requires --ws-listen")
	}
	var pipeline *ingest.Pipeline
	// Workers start only after the pipeline and optional sink have been initialized.
	recv, err := ingest.NewReceiver(cfg.recvPort, cfg.sendPort, cfg.ps5, cfg.heartbeat, func(p []byte, ts time.Time) { pipeline.Handle(p, ts) })
	if err != nil {
		return err
	}
	defer func() { _ = recv.Close() }()
	served := false
	if srv != nil {
		// Bind (and load TLS) before any worker or the raw sink starts.
		if err := srv.Listen(); err != nil {
			return err
		}
		defer func() {
			if !served { // a later startup step failed: Serve on a done context just closes the listener
				done, cancel := context.WithCancel(context.Background())
				cancel()
				_ = srv.Serve(done)
			}
		}()
	}
	var raw rawsink.RawSink
	if cfg.rawDumpDir != "" {
		path := filepath.Join(cfg.rawDumpDir, fmt.Sprintf("raw-%d.pcap", time.Now().Unix()))
		ps5, err := netip.ParseAddr(cfg.ps5)
		if err != nil {
			return err
		}
		src := netip.AddrPortFrom(ps5, uint16(cfg.sendPort))
		dst := recv.LocalAddr().(*net.UDPAddr).AddrPort()
		sink, err := rawsink.NewLocalSink(afero.NewOsFs(), path, cfg.rawBuffer, src, dst)
		if err != nil {
			return fmt.Errorf("raw sink: %w", err)
		}
		sink.SetLogger(log)
		raw = sink
		log.Info("raw dump enabled", zap.String("path", path))
	}
	var dst ingest.Publisher = outs
	if len(outs) == 1 {
		dst = outs[0]
	}
	pipeline = ingest.NewPipeline(raw, dst)
	recv.SetLogger(log)
	log.Info("collector starting", zap.String("ps5", cfg.ps5), zap.String("rtps", cfg.rtps),
		zap.Stringer("recv_addr", recv.LocalAddr()), zap.Int("send_port", cfg.sendPort))
	if srv != nil {
		logBrowserStream(log, cfg.ws, srv.Addr())
	}
	counters := func(msg string) { logCounters(log, msg, pipeline, pub, raw, hub, srv) }

	// Only the receiver sees the signal context (through recvCtx, which a failed browser stream
	// also cancels). Its return closes browser clients, then cancels the publisher, ensuring no
	// producer remains active when publishing stops.
	recvCtx, cancelRecv := context.WithCancel(ctx)
	defer cancelRecv()
	pubCtx, cancelPub := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelPub()
	wsCtx, cancelWS := context.WithCancel(pubCtx)
	defer cancelWS()
	srvDone := make(chan struct{})
	var g errgroup.Group
	g.Go(func() error {
		defer cancelPub()
		err := recv.Run(recvCtx)
		cancelWS()
		if srv != nil {
			<-srvDone
		}
		if !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	if pub != nil {
		g.Go(func() error {
			if err := pub.Run(pubCtx); !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		})
	}
	if srv != nil {
		served = true
		g.Go(func() error { _ = hub.Run(wsCtx); return nil }) // returns only wsCtx.Err()
		g.Go(func() error {
			defer close(srvDone)
			if err := serveWS(srv, wsCtx); err != nil {
				cancelRecv()
				return fmt.Errorf("browser stream: %w", err)
			}
			return nil
		})
	}
	g.Go(func() error { report(pubCtx, counters); return nil })
	runErr := g.Wait()
	if raw != nil {
		if err := raw.Close(); err != nil {
			log.Warn("raw sink close", zap.Error(err))
		}
	}
	counters("collector stopped")
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// newBrowserStream builds the hub and server for w; nothing is bound yet.
func newBrowserStream(w *wsConfig, log *zap.Logger) (*browserstream.Hub, *browserstream.Server, error) {
	id, err := browserstream.NewSessionID()
	if err != nil {
		return nil, nil, fmt.Errorf("browser stream session id: %w", err)
	}
	hub, err := browserstream.NewHub(browserstream.HubConfig{SessionID: id, CollectorVersion: version,
		ClientBuffer: w.clientBuffer, SourceTimeout: w.sourceTimeout})
	if err != nil {
		return nil, nil, err
	}
	srv, err := browserstream.NewServer(hub, browserstream.ServerConfig{Addr: w.bindAddr, Loopback: w.loopback,
		Origins: w.origins, Token: w.token, MaxClients: w.maxClients, WriteTimeout: w.writeTimeout,
		TLSCertFile: w.tlsCert, TLSKeyFile: w.tlsKey, Log: log})
	return hub, srv, err
}

// logBrowserStream reports the bound listener and its trust assumptions. The token is never logged.
func logBrowserStream(log *zap.Logger, w *wsConfig, addr net.Addr) {
	tls := w.tlsCert != ""
	log.Info("browser stream listening", zap.Stringer("addr", addr), zap.Bool("loopback", w.loopback),
		zap.Bool("tls", tls), zap.Strings("origins", w.origins), zap.Bool("token_set", w.token != ""))
	if !w.loopback && !tls {
		log.Warn("browser stream is plaintext on a non-loopback address; input telemetry and the access token cross the network unencrypted")
	}
	if w.loopback && w.token == "" {
		log.Info("browser stream has no GT7_WS_TOKEN; any local process and any allowlisted origin can read input telemetry")
	}
}

// logCounters reports pipeline accounting, the bounded-buffer drop counts and, when enabled, the
// browser stream counters. pub_dropped is omitted when RTPS is disabled.
func logCounters(log *zap.Logger, msg string, p *ingest.Pipeline, pub *transport.Publisher, raw rawsink.RawSink,
	hub *browserstream.Hub, srv *browserstream.Server) {
	c := p.Counters()
	var rawDropped uint64
	if sink, ok := raw.(*rawsink.LocalSink); ok {
		rawDropped = sink.Dropped()
	}
	fields := []zap.Field{zap.Uint64("published", c.Published), zap.Uint64("valid", c.Valid),
		zap.Uint64("foreign", c.Foreign), zap.Uint64("short", c.Short), zap.Uint64("stale", c.Stale),
		zap.Uint64("resets", c.Resets), zap.Uint64("gaps", c.Gaps)}
	if pub != nil {
		fields = append(fields, zap.Uint64("pub_dropped", pub.Dropped()))
	}
	fields = append(fields, zap.Uint64("raw_dropped", rawDropped))
	if srv != nil {
		h, s := hub.Stats(), srv.Stats()
		fields = append(fields, zap.Int("ws_clients", s.Active), zap.Uint64("ws_accepted", h.Accepted),
			zap.Uint64("ws_rejected_origin", s.RejectedOrigin), zap.Uint64("ws_rejected_token", s.RejectedToken),
			zap.Uint64("ws_rejected_subprotocol", s.RejectedSubprotocol), zap.Uint64("ws_rejected_capacity", s.RejectedCapacity),
			zap.Uint64("ws_rejected_host", s.RejectedHost), zap.Uint64("ws_sent", h.Sent), zap.Uint64("ws_dropped", h.Dropped),
			zap.Uint64("ws_slow_closed", h.SlowClosed), zap.Uint64("ws_invalid_inputs", h.InvalidInputs),
			zap.Uint64("ws_stale_discarded", h.StaleDiscarded), zap.String("ws_source_state", string(h.State)))
	}
	log.Info(msg, fields...)
}

// report keeps logging off the receive path and emits an INFO snapshot every ten seconds.
func report(ctx context.Context, counters func(msg string)) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			counters("telemetry stats")
		}
	}
}

// serveWS runs the browser stream server; tests replace it to inject a Serve failure.
var serveWS = (*browserstream.Server).Serve
