package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/browserstream"
	"github.com/coder/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const (
	wsOrigin = "http://localhost:5173"
	wsToken  = "WSSECRETtoken0123456789abcdefghijklmnop"
)

func TestParseFlagsWS(t *testing.T) {
	t.Setenv("INGEST_TOKEN", "")
	ws := []string{"--ps5=127.0.0.1", "--ws-listen=127.0.0.1:8765", "--ws-origin=" + wsOrigin}
	lan := []string{"--ps5=127.0.0.1", "--ws-listen=0.0.0.0:8765", "--ws-origin=" + wsOrigin}
	with := func(base []string, extra ...string) []string { return append(slices.Clone(base), extra...) }
	for _, tt := range []struct {
		name, env, wantErr string
		args               []string
	}{
		{"rtps none without ws", "", "--rtps=none requires --ws-listen", []string{"--ps5=127.0.0.1", "--rtps=none"}},
		{"rtps none with rtps token", "", "--rtps-token", with(ws, "--rtps=none", "--rtps-token=abc")},
		{"origin without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-origin=" + wsOrigin}},
		{"max clients without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-max-clients=8"}},
		{"buffer without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-client-buffer=8"}},
		{"write timeout without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-write-timeout=3s"}},
		{"source timeout without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-source-timeout=3s"}},
		{"insecure without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-allow-insecure-lan"}},
		{"tls without listen", "", "require --ws-listen", []string{"--ps5=127.0.0.1", "--ws-tls-cert=c.pem", "--ws-tls-key=k.pem"}},
		{"missing origin", "", "--ws-origin", []string{"--ps5=127.0.0.1", "--ws-listen=127.0.0.1:8765"}},
		{"wildcard origin", "", "--ws-origin", with(ws, "--ws-origin=*")},
		{"null origin", "", "--ws-origin", with(ws, "--ws-origin=null")},
		{"origin with path", "", "--ws-origin", with(ws, "--ws-origin=https://x.test/path")},
		{"origin trailing slash", "", "--ws-origin", with(ws, "--ws-origin=https://x.test/")},
		{"ftp origin", "", "--ws-origin", with(ws, "--ws-origin=ftp://x.test")},
		{"comma origin not split", "", "--ws-origin", []string{"--ps5=127.0.0.1", "--ws-listen=127.0.0.1:8765", "--ws-origin=http://a.test,http://b.test"}},
		{"hostname listen", "", "--ws-listen", []string{"--ps5=127.0.0.1", "--ws-listen=example.com:8765", "--ws-origin=" + wsOrigin}},
		{"listen without port", "", "--ws-listen", []string{"--ps5=127.0.0.1", "--ws-listen=127.0.0.1", "--ws-origin=" + wsOrigin}},
		{"non-loopback without token", "", "GT7_WS_TOKEN", lan},
		{"non-loopback plaintext without opt-in", wsToken, "--ws-allow-insecure-lan", lan},
		{"non-loopback insecure opt-in", wsToken, "", with(lan, "--ws-allow-insecure-lan")},
		{"non-loopback tls", wsToken, "", with(lan, "--ws-tls-cert=c.pem", "--ws-tls-key=k.pem")},
		{"tls cert only", "", "--ws-tls-key", with(ws, "--ws-tls-cert=c.pem")},
		{"tls key only", "", "--ws-tls-cert", with(ws, "--ws-tls-key=k.pem")},
		{"max clients low", "", "--ws-max-clients", with(ws, "--ws-max-clients=0")},
		{"max clients high", "", "--ws-max-clients", with(ws, "--ws-max-clients=65")},
		{"buffer low", "", "--ws-client-buffer", with(ws, "--ws-client-buffer=0")},
		{"buffer high", "", "--ws-client-buffer", with(ws, "--ws-client-buffer=4097")},
		{"write timeout low", "", "--ws-write-timeout", with(ws, "--ws-write-timeout=99ms")},
		{"write timeout high", "", "--ws-write-timeout", with(ws, "--ws-write-timeout=61s")},
		{"source timeout low", "", "--ws-source-timeout", with(ws, "--ws-source-timeout=99ms")},
		{"source timeout high", "", "--ws-source-timeout", with(ws, "--ws-source-timeout=61s")},
		{"bad token syntax", "short-secret", "GT7_WS_TOKEN", ws},
		{"bad token char", strings.Repeat("s", 31) + "!secret", "GT7_WS_TOKEN", ws},
		{"range limits ok", "", "", with(ws, "--ws-max-clients=64", "--ws-client-buffer=4096", "--ws-write-timeout=100ms", "--ws-source-timeout=60s")},
		{"ws only", "", "", with(ws, "--rtps=none")},
		{"dual", "", "", with(ws, "--rtps=localhost:50051")},
		{"loopback with token", wsToken, "", ws},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GT7_WS_TOKEN", tt.env)
			cfg, err := parseFlags(tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if cfg.ws == nil {
					t.Fatal("ws disabled")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.env != "" && strings.Contains(err.Error(), "secret") {
				t.Fatalf("token leaked: %v", err)
			}
		})
	}
}

func TestParseFlagsWSConfig(t *testing.T) {
	t.Setenv("INGEST_TOKEN", "ingest-secret")
	t.Setenv("GT7_WS_TOKEN", wsToken)
	cfg, err := parseFlags([]string{"--ps5=127.0.0.1", "--rtps=none", "--ws-listen=localhost:8765",
		"--ws-origin=HTTP://LOCALHOST:5173", "--ws-origin=" + wsOrigin, "--ws-origin=https://trainer.example.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.rtps != "none" || cfg.rtpsToken != "" {
		t.Fatalf("rtps = %q, token cleared = %v", cfg.rtps, cfg.rtpsToken == "")
	}
	want := wsConfig{bindAddr: "127.0.0.1:8765", loopback: true, origins: []string{wsOrigin, "https://trainer.example.com"},
		token: wsToken, maxClients: 4, clientBuffer: 64, writeTimeout: 2 * time.Second, sourceTimeout: 2 * time.Second}
	if cfg.ws == nil || fmt.Sprint(*cfg.ws) != fmt.Sprint(want) {
		t.Fatal("unexpected ws config") // not printed: it holds the token
	}
}

func TestParseFlagsLegacyDisablesWS(t *testing.T) {
	t.Setenv("GT7_WS_TOKEN", wsToken) // set globally, ignored without --ws-listen
	cfg, err := parseFlags([]string{"--ps5=192.0.2.5"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ws != nil || cfg.rtps != "localhost:50051" {
		t.Fatalf("legacy config changed: ws=%v rtps=%q", cfg.ws != nil, cfg.rtps)
	}
}

func TestWSTokenRedactedInParseErrors(t *testing.T) {
	t.Setenv("GT7_WS_TOKEN", wsToken)
	for _, args := range [][]string{
		{"--ps5=127.0.0.1", "--heartbeat=" + wsToken},
		{"--ps5=127.0.0.1", "--ws-listen=127.0.0.1:8765", "--ws-origin=http://" + wsToken + "/x"},
		{"--ps5=" + wsToken},
	} {
		_, err := parseFlags(args)
		if err == nil || strings.Contains(err.Error(), wsToken) {
			t.Errorf("unsafe parse error: %v", err)
		}
	}
}

func TestMainExitWSCodes(t *testing.T) {
	if got := mainExit([]string{"--ps5=127.0.0.1", "--rtps=none"}); got != 2 {
		t.Errorf("rtps none without ws exit = %d, want 2", got)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	// os/signal starts one permanent goroutine on first use; start it before the baseline.
	_, stop := signal.NotifyContext(context.Background(), syscall.SIGINT)
	stop()
	base := runtime.NumGoroutine()
	args := []string{"--ps5=127.0.0.1", "--recv-port=" + fmt.Sprint(freeUDPPort(t)), "--rtps=none",
		"--ws-listen=" + busy.Addr().String(), "--ws-origin=" + wsOrigin, "--log-level=error"}
	if got := mainExit(args); got != 1 {
		t.Errorf("occupied ws port exit = %d, want 1", got)
	}
	settle(t, base)
}

func TestRunLegacyBindsNoListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	core, logs := observer.New(zapcore.DebugLevel)
	cfg := config{ps5: "127.0.0.1", rtps: "localhost:50051", sendPort: 33739, rawBuffer: 8, pubBuffer: 8, heartbeat: time.Second}
	if err := run(ctx, cfg, zap.New(core)); err != nil {
		t.Fatal(err)
	}
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "browser stream") {
			t.Fatalf("unexpected log %q", e.Message)
		}
	}
	final := lastEntry(t, logs, "collector stopped")
	if _, ok := final["pub_dropped"]; !ok {
		t.Error("pub_dropped missing")
	}
	for k := range final {
		if strings.HasPrefix(k, "ws_") {
			t.Errorf("unexpected field %s", k)
		}
	}
}

func TestRunWSOnly(t *testing.T) {
	base := runtime.NumGoroutine()
	e := startRun(t, wsCfg("none"))
	c := dialCollector(t, e.url, "")
	sendFixtures(t, e.udp)
	wantSamples(t, c, goldenInputs(t))
	e.cancel()
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	_, _, err := c.Read(rctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
		t.Errorf("close status = %v (%v), want 1001", got, err)
	}
	if err := e.result(); err != nil {
		t.Fatal(err)
	}
	if n := e.logs.FilterLevelExact(zapcore.WarnLevel).Len(); n != 0 {
		t.Errorf("unexpected warnings: %v", e.logs.FilterLevelExact(zapcore.WarnLevel).All())
	}
	if e.logs.FilterMessageSnippet("any local process").Len() != 1 {
		t.Error("missing loopback no-token note")
	}
	final := lastEntry(t, e.logs, "collector stopped")
	if _, ok := final["pub_dropped"]; ok {
		t.Error("pub_dropped present with RTPS disabled")
	}
	for _, k := range []string{"ws_clients", "ws_accepted", "ws_rejected_origin", "ws_rejected_token", "ws_rejected_subprotocol",
		"ws_rejected_capacity", "ws_rejected_host", "ws_sent", "ws_dropped", "ws_slow_closed", "ws_invalid_inputs",
		"ws_stale_discarded", "ws_source_state"} {
		if _, ok := final[k]; !ok {
			t.Errorf("final counters missing %s", k)
		}
	}
	if final["ws_accepted"] != uint64(1) || final["published"] != uint64(8) {
		t.Errorf("final counters: %v", final)
	}
	_ = c.CloseNow()
	settle(t, base)
}

func TestRunDualMode(t *testing.T) {
	lines := make(chan string, 64)
	rtps := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s := bufio.NewScanner(r.Body)
		for s.Scan() {
			lines <- s.Text()
		}
	}))
	defer rtps.Close()
	e := startRun(t, wsCfg(rtps.URL+"/ingest"))
	defer e.stop(t)
	c := dialCollector(t, e.url, "")
	want := goldenInputs(t)
	sendFixtures(t, e.udp)
	wantSamples(t, c, want)
	for i, g := range want {
		select {
		case l := <-lines:
			if !strings.Contains(l, fmt.Sprintf(`"packageId":%d`, g.PackageID)) {
				t.Errorf("rtps frame %d = %s, want packageId %d", i, l, g.PackageID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("rtps received %d of %d frames", i, len(want))
		}
	}
}

func TestRunRTPSUnreachableWSContinues(t *testing.T) {
	e := startRun(t, wsCfg("127.0.0.1:1"))
	defer e.stop(t)
	c := dialCollector(t, e.url, "")
	sendFixtures(t, e.udp)
	wantSamples(t, c, goldenInputs(t))
}

func TestRunWSTokenNeverLogged(t *testing.T) {
	cfg := wsCfg("none")
	cfg.ws.token = wsToken
	e := startRun(t, cfg)
	// Wrong and right tokens: the rejection is logged at DEBUG, without the offered value.
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	_, _, err := websocket.Dial(dctx, e.url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {wsOrigin}}, Subprotocols: []string{browserstream.Protocol, "gt7.token." + wsToken + "x"}})
	if err == nil {
		t.Fatal("wrong token accepted")
	}
	c := dialCollector(t, e.url, wsToken)
	sendFixtures(t, e.udp)
	wantSamples(t, c, goldenInputs(t))
	e.cancel()
	if err := e.result(); err != nil {
		t.Fatal(err)
	}
	if e.logs.FilterField(zap.Bool("token_set", true)).Len() != 1 {
		t.Error("token_set not logged")
	}
	for _, en := range e.logs.All() {
		if strings.Contains(fmt.Sprint(en.Message, en.ContextMap()), wsToken) {
			t.Fatalf("token leaked in log %q", en.Message)
		}
	}
}

func TestRunWSBindFailure(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	dir := t.TempDir()
	base := runtime.NumGoroutine()
	for name, mod := range map[string]func(*wsConfig){
		"occupied port": func(w *wsConfig) { w.bindAddr = busy.Addr().String() },
		"missing tls":   func(w *wsConfig) { w.tlsCert, w.tlsKey = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem") },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := wsCfg("none")
			cfg.rawDumpDir = dir
			mod(cfg.ws)
			if err := run(context.Background(), cfg, zap.NewNop()); err == nil {
				t.Fatal("run succeeded")
			}
		})
	}
	settle(t, base)
	if paths, _ := filepath.Glob(filepath.Join(dir, "raw-*.pcap")); len(paths) != 0 {
		t.Errorf("raw sink opened before ws bind: %v", paths)
	}
}

func TestRunNonLoopbackPlaintextWarns(t *testing.T) {
	cfg := wsCfg("none")
	cfg.ws.bindAddr, cfg.ws.loopback, cfg.ws.token, cfg.ws.allowInsecureLAN = "0.0.0.0:0", false, wsToken, true
	e := startRun(t, cfg)
	e.cancel()
	if err := e.result(); err != nil {
		t.Fatal(err)
	}
	if e.logs.FilterMessageSnippet("plaintext on a non-loopback address").FilterLevelExact(zapcore.WarnLevel).Len() != 1 {
		t.Error("missing plaintext warning")
	}
}

// --- helpers ---

type runEnv struct {
	url, udp string
	logs     *observer.ObservedLogs
	cancel   context.CancelFunc
	errc     chan error
	once     sync.Once
	err      error
}

// result waits (bounded, once) for run to return.
func (e *runEnv) result() error {
	e.once.Do(func() {
		select {
		case e.err = <-e.errc:
		case <-time.After(10 * time.Second):
			e.err = errTimeout
		}
	})
	return e.err
}

var errTimeout = errors.New("run did not return within 10s")

// stop cancels the collector and requires a clean return.
func (e *runEnv) stop(t *testing.T) {
	t.Helper()
	e.cancel()
	if err := e.result(); err != nil {
		t.Error(err)
	}
}

func wsCfg(rtps string) config {
	return config{ps5: "127.0.0.1", rtps: rtps, sendPort: 33739, rawBuffer: 8, pubBuffer: 64, heartbeat: time.Second,
		ws: &wsConfig{bindAddr: "127.0.0.1:0", loopback: true, origins: []string{wsOrigin}, maxClients: 4, clientBuffer: 64,
			writeTimeout: 2 * time.Second, sourceTimeout: 2 * time.Second}}
}

// startRun runs the collector and waits for both listeners from the startup logs.
func startRun(t *testing.T, cfg config) *runEnv {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	ctx, cancel := context.WithCancel(context.Background())
	e := &runEnv{logs: logs, cancel: cancel, errc: make(chan error, 1)}
	go func() { e.errc <- run(ctx, cfg, zap.New(core)) }()
	t.Cleanup(func() { // idempotent: cancel may repeat; result waits once, bounded
		cancel()
		if errors.Is(e.result(), errTimeout) {
			t.Error(errTimeout)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for e.url == "" || e.udp == "" {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("collector did not start: %v", e.result())
		}
		if l := logs.FilterMessage("browser stream listening").All(); len(l) == 1 {
			e.url = "ws://" + l[0].ContextMap()["addr"].(string) + "/v1/stream"
		}
		if l := logs.FilterMessage("collector starting").All(); len(l) == 1 {
			_, port, _ := net.SplitHostPort(l[0].ContextMap()["recv_addr"].(string))
			e.udp = net.JoinHostPort("127.0.0.1", port)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return e
}

type wsMsg struct {
	Type, Status, State, Reason, SessionID, CollectorVersion string
	PackageID                                                uint32
	Inputs                                                   struct{ Brake, Throttle *float32 }
}

func readWS(t *testing.T, c *websocket.Conn) wsMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m wsMsg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// dialCollector connects with the allowlisted origin and checks hello.
func dialCollector(t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	protos := []string{browserstream.Protocol}
	if token != "" {
		protos = append(protos, "gt7.token."+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {wsOrigin}}, Subprotocols: protos})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	h := readWS(t, c)
	if h.Type != "hello" || h.Status != string(browserstream.StateWaiting) || len(h.SessionID) != 32 || h.CollectorVersion != version {
		t.Fatalf("hello = %+v", h)
	}
	return c
}

type golden struct {
	PackageID       uint32
	Brake, Throttle float32
}

func goldenInputs(t *testing.T) []golden {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "protocol", "testdata", "golden", "packet_0*.bin.json"))
	if err != nil || len(paths) != 8 {
		t.Fatalf("golden: %v %d", err, len(paths))
	}
	var out []golden
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		out = append(out, g)
	}
	return out
}

// sendFixtures sends the encrypted fixtures in file order (increasing PackageID).
func sendFixtures(t *testing.T, addr string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "protocol", "testdata", "packet_0*.bin"))
	if err != nil || len(paths) != 8 {
		t.Fatalf("fixtures: %v %d", err, len(paths))
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(b); err != nil {
			t.Fatal(err)
		}
	}
}

func wantSamples(t *testing.T, c *websocket.Conn, want []golden) {
	t.Helper()
	if m := readWS(t, c); m.Type != "status" || m.State != string(browserstream.StateLive) || m.Reason != browserstream.ReasonFirstFrame {
		t.Fatalf("status = %+v", m)
	}
	for i, g := range want {
		m := readWS(t, c)
		if m.Type != "sample" || m.PackageID != g.PackageID || m.Inputs.Brake == nil || m.Inputs.Throttle == nil ||
			*m.Inputs.Brake != g.Brake || *m.Inputs.Throttle != g.Throttle {
			t.Fatalf("sample %d = %+v, want %+v", i, m, g)
		}
	}
}

func lastEntry(t *testing.T, logs *observer.ObservedLogs, msg string) map[string]any {
	t.Helper()
	l := logs.FilterMessage(msg).All()
	if len(l) == 0 {
		t.Fatalf("no %q log", msg)
	}
	return l[len(l)-1].ContextMap()
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// settle waits for the goroutine count to return to base (no leak).
func settle(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// captureStderr runs f with os.Stderr redirected and returns what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	out := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); out <- string(b) }()
	f()
	os.Stderr = old
	_ = w.Close()
	select {
	case s := <-out:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("stderr reader did not finish")
		return ""
	}
}

func TestRunErrorRedacted(t *testing.T) {
	const rtpsTok = "rtps-secret-token"
	t.Setenv("INGEST_TOKEN", "")
	t.Setenv("GT7_WS_TOKEN", wsToken)
	for name, tc := range map[string]struct {
		tok  string
		args []string
	}{
		"ws token":   {wsToken, []string{"--rtps=none", "--ws-tls-cert=/nonexistent/" + wsToken + "/c.pem"}},
		"rtps token": {rtpsTok, []string{"--rtps-token=" + rtpsTok, "--ws-tls-cert=/nonexistent/" + rtpsTok + "/c.pem"}},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"--ps5=127.0.0.1", "--recv-port=" + fmt.Sprint(freeUDPPort(t)),
				"--ws-listen=127.0.0.1:1", "--ws-origin=" + wsOrigin, "--ws-tls-key=/nonexistent/k.pem"}, tc.args...)
			var code int
			out := captureStderr(t, func() { code = mainExit(args) })
			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if strings.Contains(out, tc.tok) || !strings.Contains(out, "[REDACTED]") {
				t.Errorf("run error not redacted: %s", strings.ReplaceAll(out, tc.tok, "<TOKEN>"))
			}
		})
	}
}

func TestQuotedTokenRedacted(t *testing.T) {
	const tok = `q"uo\te-secret`
	t.Setenv("INGEST_TOKEN", "")
	_, err := parseFlags([]string{"--ps5=127.0.0.1", "--heartbeat=" + tok, "--rtps-token=" + tok})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("quoted token leaked in parse error: %v", err)
	}
	if err := redact(fmt.Errorf("a %q b %s", tok, tok), tok); strings.Contains(err.Error(), "secret") {
		t.Errorf("redact left %v", err)
	}
	if redact(nil, tok) != nil {
		t.Error("redact(nil) != nil")
	}
	orig := fmt.Errorf("wrapped: %w", context.Canceled)
	if err := redact(orig, tok, ""); err != orig {
		t.Error("unchanged error was replaced")
	}
}

func TestRunRawSinkFailureClosesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // ponytail: port reuse race; flakes only if another process grabs the port
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base := runtime.NumGoroutine()
	cfg := wsCfg("none")
	cfg.ws.bindAddr, cfg.rawDumpDir = addr, filepath.Join(file, "sub")
	if err := run(context.Background(), cfg, zap.NewNop()); err == nil || !strings.Contains(err.Error(), "raw sink") {
		t.Fatalf("run error = %v, want raw sink failure", err)
	}
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("ws listener not released: %v", err)
	}
	_ = again.Close()
	settle(t, base)
}

func TestRunServeFailureStopsReceiver(t *testing.T) {
	errBoom := errors.New("boom")
	old := serveWS
	t.Cleanup(func() { serveWS = old })
	serveWS = func(s *browserstream.Server, ctx context.Context) error {
		done, cancel := context.WithCancel(ctx)
		cancel()
		_ = s.Serve(done) // releases the listener
		return errBoom
	}
	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, wsCfg("none"), zap.NewNop()) }()
	select {
	case err := <-errc:
		if !errors.Is(err, errBoom) {
			t.Fatalf("run error = %v, want boom", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		select {
		case err := <-errc:
			t.Fatalf("receiver kept running after the browser stream failed (returned %v on cancel)", err)
		case <-time.After(10 * time.Second):
			t.Fatal("receiver kept running after the browser stream failed, and ignored cancel")
		}
	}
	settle(t, base)
}
