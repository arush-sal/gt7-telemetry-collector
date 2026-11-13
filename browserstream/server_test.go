package browserstream

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const testOrigin = "https://trainer.example.com"

var (
	testToken  = "SECRETtoken" + strings.Repeat("x", 30)
	wrongToken = "WRONGtoken" + strings.Repeat("y", 30)
)

type testEnv struct {
	s    *Server
	h    *Hub
	ts   *httptest.Server
	url  string
	logs *observer.ObservedLogs
}

func baseConfig(logs **observer.ObservedLogs) ServerConfig {
	core, ol := observer.New(zapcore.DebugLevel)
	*logs = ol
	return ServerConfig{
		Addr:         "127.0.0.1:0",
		Loopback:     true,
		Origins:      []string{testOrigin},
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
		Log:          zap.New(core),
	}
}

// newEnv serves s.Handler() via httptest. mod may adjust the config.
func newEnv(t *testing.T, mod func(*ServerConfig)) *testEnv {
	t.Helper()
	e := &testEnv{h: newRealHub(t, 4096, time.Minute)}
	cfg := baseConfig(&e.logs)
	if mod != nil {
		mod(&cfg)
	}
	s, err := NewServer(e.h, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	e.ts = httptest.NewServer(s.Handler())
	e.url = "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/v1/stream"
	t.Cleanup(func() {
		waitFor(t, "clients to drain", func() bool { return s.Stats().Active == 0 && e.h.Stats().Clients == 0 })
		e.ts.Close()
	})
	return e
}

func dialOpts(origin string, protos ...string) *websocket.DialOptions {
	o := &websocket.DialOptions{HTTPHeader: http.Header{}, Subprotocols: protos}
	if origin != "" {
		o.HTTPHeader.Set("Origin", origin)
	}
	return o
}

func dialWS(t *testing.T, url string, o *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, url, o)
	if c != nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

// mustDial connects with the default origin and protocol and consumes hello.
func mustDial(t *testing.T, url string, protos ...string) *websocket.Conn {
	t.Helper()
	if len(protos) == 0 {
		protos = []string{Protocol}
	}
	c, resp, err := dialWS(t, url, dialOpts(testOrigin, protos...))
	if err != nil {
		t.Fatalf("dial: %v (status %v)", err, status(resp))
	}
	if got := resp.Header.Values("Sec-WebSocket-Protocol"); len(got) != 1 || got[0] != Protocol {
		t.Fatalf("negotiated %q, want %q", got, Protocol)
	}
	wantHello(t, readMsg(t, c), StateWaiting)
	return c
}

func status(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func readMsg(t *testing.T, c *websocket.Conn) msg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("message type %v", typ)
	}
	return decode(t, b)
}

// readUntilClose reads until an error and returns its close status.
func readUntilClose(c *websocket.Conn, d time.Duration) (websocket.StatusCode, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				return ce.Code, ce.Reason, nil
			}
			return -1, "", err
		}
	}
}

// raw sends a handshake-shaped request (or a plain request when upgrade is false).
func raw(t *testing.T, base, method, path, host string, hdr http.Header, upgrade bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if host != "" {
		req.Host = host
	}
	if upgrade {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		_ = resp.Body.Close()
		return resp, ""
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

func handshake(origin string, protos ...string) http.Header {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	for _, p := range protos {
		h.Add("Sec-WebSocket-Protocol", p)
	}
	return h
}

func TestHappyPath(t *testing.T) {
	e := newEnv(t, nil)
	c := mustDial(t, e.url)
	for seq := uint64(1); seq <= 5; seq++ {
		e.h.Publish(frame(seq, 10, 20))
	}
	wantStatus(t, readMsg(t, c), StateLive, ReasonFirstFrame)
	for seq := uint64(1); seq <= 5; seq++ {
		wantSample(t, readMsg(t, c), seq, 0)
	}
	if st := e.s.Stats(); st.Active != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestHealthzAndRouting(t *testing.T) {
	e := newEnv(t, nil)
	resp, body := raw(t, e.ts.URL, http.MethodGet, "/healthz", "", nil, false)
	if resp.StatusCode != 200 || body != `{"status":"ok"}` ||
		resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("healthz %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS header sent")
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/healthz", 405},
		{http.MethodPost, "/v1/stream", 405},
		{http.MethodPut, "/v1/stream", 405},
		{http.MethodGet, "/", 404},
		{http.MethodGet, "/v1/stream/", 404},
		{http.MethodGet, "/healthz/x", 404},
	} {
		if resp, _ := raw(t, e.ts.URL, c.method, c.path, "", nil, false); resp.StatusCode != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
	if st := e.s.Stats(); st.RejectedMethod != 2 {
		t.Fatalf("RejectedMethod %d, want 2", st.RejectedMethod)
	}
}

func TestOriginMatrix(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) {
		c.Origins = []string{testOrigin, "http://[::1]:5173", "HTTP://LocalHost:80"}
	})
	reject := []string{
		"https://evil.test", "null", "", "https://trainer.example.com.evil.test", "https://trainer.example.co",
		"https://xtrainer.example.com", "https://trainer.example.com:8443", "http://trainer.example.com",
		"https://trainer.example.com/", "https://*.example.com", "http://[::1]:5174", "http://localhost:8080",
	}
	for _, o := range reject {
		_, resp, err := dialWS(t, e.url, dialOpts(o, Protocol))
		if err == nil || status(resp) != http.StatusForbidden {
			t.Errorf("origin %q: status %d err %v, want 403", o, status(resp), err)
		}
	}
	// Two Origin header values are rejected.
	h := handshake(testOrigin, Protocol)
	h.Add("Origin", testOrigin)
	if resp, _ := raw(t, e.ts.URL, http.MethodGet, "/v1/stream", "", h, true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("two origins: %d", resp.StatusCode)
	}
	if st := e.s.Stats(); st.RejectedOrigin != uint64(len(reject)+1) || st.Active != 0 {
		t.Fatalf("stats %+v", st)
	}
	for _, o := range []string{"HTTPS://Trainer.Example.COM", "https://trainer.example.com:443", "http://[::1]:5173",
		"http://localhost", "http://localhost:80", testOrigin} {
		c, resp, err := dialWS(t, e.url, dialOpts(o, Protocol))
		if err != nil {
			t.Errorf("origin %q: status %d err %v, want accepted", o, status(resp), err)
			continue
		}
		wantHello(t, readMsg(t, c), StateWaiting)
		_ = c.CloseNow()
	}
}

func TestHostMatrix(t *testing.T) {
	e := newEnv(t, nil)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(e.ts.URL, "http://"))
	for _, host := range []string{"evil.test", "evil.test:" + port, "localhost.", "localhost.:" + port, "10.0.0.1",
		"10.0.0.1:" + port, "127.0.0.1.evil.test", "[fe80::1]:" + port, "0.0.0.0:" + port} {
		for _, path := range []string{"/healthz", "/v1/stream", "/nope"} {
			resp, _ := raw(t, e.ts.URL, http.MethodGet, path, host, handshake(testOrigin, Protocol), path == "/v1/stream")
			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("Host %q %s = %d, want 421", host, path, resp.StatusCode)
			}
		}
	}
	if st := e.s.Stats(); st.RejectedHost != 27 {
		t.Fatalf("RejectedHost %d, want 27", st.RejectedHost)
	}
	for _, host := range []string{"127.0.0.1:" + port, "localhost:" + port, "LOCALHOST", "[::1]:" + port, "[::1]",
		"127.0.0.2", "[::ffff:127.0.0.1]:" + port} {
		if resp, _ := raw(t, e.ts.URL, http.MethodGet, "/healthz", host, nil, false); resp.StatusCode != 200 {
			t.Errorf("Host %q = %d, want 200", host, resp.StatusCode)
		}
	}
	// Non-loopback mode does not restrict Host.
	e2 := newEnv(t, func(c *ServerConfig) { c.Loopback = false; c.Token = testToken })
	if resp, _ := raw(t, e2.ts.URL, http.MethodGet, "/healthz", "evil.test", nil, false); resp.StatusCode != 200 {
		t.Errorf("non-loopback Host evil.test = %d", resp.StatusCode)
	}
}

func TestSubprotocolRejections(t *testing.T) {
	e := newEnv(t, nil)
	cases := [][]string{
		nil,
		{"other"},
		{"gt7.inputs.v1, gt7.inputs.v1"},
		{"gt7.inputs.v1", "gt7.inputs.v1"},
		{"GT7.INPUTS.V1"},
		{"gt7.inputs.v1,a,b,c,d,e,f,g,h"},
		{"gt7.inputs.v1,"},
		{"gt7.inputs.v1", ""},
		{"gt7.inputs.v1,gt7.token.a,gt7.token.b"},
	}
	for _, protos := range cases {
		resp, _ := raw(t, e.ts.URL, http.MethodGet, "/v1/stream", "", handshake(testOrigin, protos...), true)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", protos, resp.StatusCode)
		}
	}
	if st := e.s.Stats(); st.RejectedSubprotocol != uint64(len(cases)) || st.Active != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestToken(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.Token = testToken })
	type tc struct {
		name   string
		url    string
		protos []string
		want   int
	}
	cases := []tc{
		{"none", e.url, []string{Protocol}, 401},
		{"wrong", e.url, []string{Protocol, "gt7.token." + wrongToken}, 401},
		{"prefix", e.url, []string{Protocol, "gt7.token." + testToken[:32]}, 401},
		{"empty", e.url, []string{Protocol, "gt7.token."}, 401},
		{"query", e.url + "?token=" + testToken, []string{Protocol}, 401},
		{"two", e.url, []string{Protocol, "gt7.token." + testToken, "gt7.token." + testToken}, 400},
		{"bad origin first", e.url, []string{Protocol, "gt7.token." + wrongToken}, 403},
	}
	for _, c := range cases {
		origin := testOrigin
		if c.want == 403 {
			origin = "https://evil.test"
		}
		_, resp, err := dialWS(t, c.url, dialOpts(origin, c.protos...))
		if err == nil || status(resp) != c.want {
			t.Errorf("%s: status %d err %v, want %d", c.name, status(resp), err, c.want)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		if c.want == 401 && strings.TrimSpace(string(b)) != "unauthorized" {
			t.Errorf("%s: body %q", c.name, b)
		}
		assertNoSecret(t, c.name, fmt.Sprint(resp.Header)+string(b))
	}
	c := mustDial(t, e.url, Protocol, "gt7.token."+testToken)
	_ = c.CloseNow()
	st := e.s.Stats()
	if st.RejectedToken != 5 || st.RejectedSubprotocol != 1 || st.RejectedOrigin != 1 {
		t.Fatalf("stats %+v", st)
	}
	assertLogsClean(t, e.logs, true)
}

func TestTokenIgnoredWhenNotConfigured(t *testing.T) {
	e := newEnv(t, nil)
	c, resp, err := dialWS(t, e.url, dialOpts(testOrigin, Protocol, "gt7.token."+testToken))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, "accepted", fmt.Sprint(resp.Header))
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != Protocol {
		t.Fatalf("negotiated %q", got)
	}
	wantHello(t, readMsg(t, c), StateWaiting)
	// Token first in the offer must not be selected either.
	c2, resp, err := dialWS(t, e.url, dialOpts(testOrigin, "gt7.token."+testToken, Protocol))
	if err != nil || resp.Header.Get("Sec-WebSocket-Protocol") != Protocol {
		t.Fatalf("token-first offer: %v %v", err, resp.Header)
	}
	_ = c2.CloseNow()
	assertLogsClean(t, e.logs, false)
}

func assertNoSecret(t *testing.T, where, s string) {
	t.Helper()
	for _, tok := range []string{testToken, wrongToken, "SECRETtoken", "WRONGtoken"} {
		if strings.Contains(s, tok) {
			t.Errorf("%s: contains token material: %q", where, s)
		}
	}
}

func assertLogsClean(t *testing.T, logs *observer.ObservedLogs, wantSome bool) {
	t.Helper()
	if wantSome && logs.Len() == 0 {
		t.Fatal("expected debug rejection logs")
	}
	for _, en := range logs.All() {
		assertNoSecret(t, "log", fmt.Sprint(en.Message, en.ContextMap()))
	}
}

func TestCapacity(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.MaxClients = 2; c.Token = testToken })
	good := []string{Protocol, "gt7.token." + testToken}

	// 50 concurrent unauthenticated requests never take slots.
	var wg sync.WaitGroup
	codes := make(chan int, 50)
	for i := range 50 {
		wg.Go(func() {
			o := testOrigin
			protos := []string{Protocol, "gt7.token." + wrongToken}
			if i%2 == 0 {
				o, protos = "https://evil.test", good
			}
			_, resp, _ := dialWS(t, e.url, dialOpts(o, protos...))
			codes <- status(resp)
		})
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != 401 && c != 403 {
			t.Fatalf("unauthenticated status %d", c)
		}
	}
	if st := e.s.Stats(); st.RejectedCapacity != 0 || st.Active != 0 || st.RejectedOrigin != 25 || st.RejectedToken != 25 {
		t.Fatalf("stats %+v", st)
	}

	c1 := mustDial(t, e.url, good...)
	mustDial(t, e.url, good...)
	_, resp, err := dialWS(t, e.url, dialOpts(testOrigin, good...))
	if err == nil || status(resp) != http.StatusServiceUnavailable {
		t.Fatalf("third: %d %v", status(resp), err)
	}
	if st := e.s.Stats(); st.RejectedCapacity != 1 || st.Active != 2 {
		t.Fatalf("stats %+v", st)
	}
	_ = c1.CloseNow()
	waitFor(t, "slot release", func() bool { return e.s.Stats().Active == 1 })
	mustDial(t, e.url, good...)
}

func TestReadOnly(t *testing.T) {
	e := newEnv(t, nil)
	c := mustDial(t, e.url)
	if err := c.Write(context.Background(), websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	code, reason, err := readUntilClose(c, 5*time.Second)
	if err != nil || code != websocket.StatusPolicyViolation || reason != "read_only_stream" {
		t.Fatalf("close %v %q %v, want 1008 read_only_stream", code, reason, err)
	}
	waitFor(t, "slot release", func() bool { return e.s.Stats().Active == 0 && e.h.Stats().Clients == 0 })

	c = mustDial(t, e.url)
	go func() { _ = c.Write(context.Background(), websocket.MessageBinary, make([]byte, 1<<20)) }()
	code, _, err = readUntilClose(c, 5*time.Second)
	if err != nil || code != websocket.StatusMessageTooBig {
		t.Fatalf("close %v %v, want 1009", code, err)
	}
	waitFor(t, "slot release", func() bool { return e.s.Stats().Active == 0 && e.h.Stats().Clients == 0 })
}

func TestSlowConsumer(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.WriteTimeout = 200 * time.Millisecond })
	// The slow client has a tiny receive buffer and never reads after hello.
	slowOpts := dialOpts(testOrigin, Protocol)
	slowOpts.HTTPClient = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, n, a)
		if err == nil {
			_ = conn.(*net.TCPConn).SetReadBuffer(1024)
		}
		return conn, err
	}}}
	slow, _, err := dialWS(t, e.url, slowOpts)
	if err != nil {
		t.Fatal(err)
	}
	wantHello(t, readMsg(t, slow), StateWaiting)
	fast := mustDial(t, e.url)
	wantStatusAfter := true

	// Publish in batches smaller than the client buffer and let the fast client catch up after each
	// one, so it never drops; the slow client's socket fills until a write misses its deadline.
	var prev, seq uint64
	const batch = 256
	for e.h.Stats().SlowClosed == 0 {
		if seq > 200_000 {
			t.Fatalf("slow client never closed: %+v %+v", e.s.Stats(), e.h.Stats())
		}
		for range batch {
			seq++
			e.h.Publish(frame(seq, 10, 20))
		}
		for prev < seq {
			m := readMsg(t, fast)
			if wantStatusAfter {
				wantStatus(t, m, StateLive, ReasonFirstFrame)
				wantStatusAfter = false
				continue
			}
			wantSample(t, m, prev+1, 0)
			prev++
		}
	}
	waitFor(t, "slow slot release", func() bool { return e.s.Stats().Active == 1 && e.h.Stats().Clients == 1 })
	// The fast client is still served.
	e.h.Publish(frame(seq+1, 10, 20))
	wantSample(t, readMsg(t, fast), seq+1, 0)
	if st := e.h.Stats(); st.SlowClosed != 1 {
		t.Fatalf("hub stats %+v", st)
	}
}

func TestPingTimeoutReleasesSlot(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.pingInterval, c.pingTimeout = 50*time.Millisecond, 50*time.Millisecond })
	// A healthy client (reader running, so pongs are sent) survives many intervals.
	healthy := mustDial(t, e.url)
	hctx := healthy.CloseRead(context.Background())

	start := time.Now()
	dead := mustDial(t, e.url) // never reads again: no pongs
	_ = dead
	waitFor(t, "dead peer release", func() bool { return e.s.Stats().Active == 1 })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("dead peer released after %v", d)
	}
	time.Sleep(300 * time.Millisecond)
	if hctx.Err() != nil || e.s.Stats().Active != 1 {
		t.Fatalf("healthy client closed: %v %+v", hctx.Err(), e.s.Stats())
	}
}

// listenServe starts a real server; the returned stop cancels Serve and returns its error.
func listenServe(t *testing.T, cfg ServerConfig, h *Hub) (*Server, func() error) {
	t.Helper()
	s, err := NewServer(h, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	var once sync.Once
	var serr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case serr = <-done:
			case <-time.After(cfg.WriteTimeout + 2*time.Second):
				serr = errors.New("Serve did not return")
			}
		})
		return serr
	}
	t.Cleanup(func() { _ = stop() })
	return s, stop
}

func settle(t *testing.T, before int) {
	t.Helper()
	http.DefaultClient.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines %d > %d\n%s", runtime.NumGoroutine(), before, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShutdown(t *testing.T) {
	before := runtime.NumGoroutine()
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.WriteTimeout = 500 * time.Millisecond
	h := newRealHub(t, 64, time.Minute)
	s, stop := listenServe(t, cfg, h)
	url := "ws://" + s.Addr().String() + "/v1/stream"
	type res struct {
		code   websocket.StatusCode
		reason string
		err    error
	}
	results := make(chan res, 2)
	for range 2 {
		c := mustDial(t, url)
		go func() {
			code, reason, err := readUntilClose(c, 5*time.Second)
			results <- res{code, reason, err}
		}()
	}
	waitFor(t, "clients", func() bool { return s.Stats().Active == 2 })
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if d := time.Since(start); d > cfg.WriteTimeout+time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	for range 2 {
		r := <-results
		if r.err != nil || r.code != websocket.StatusGoingAway || r.reason != "collector_shutdown" {
			t.Fatalf("close %+v, want 1001 collector_shutdown", r)
		}
	}
	if st := s.Stats(); st.Active != 0 || h.Stats().Clients != 0 {
		t.Fatalf("stats %+v %+v", st, h.Stats())
	}
	settle(t, before)
}

func TestShutdownStuckClientBounded(t *testing.T) {
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.WriteTimeout = 200 * time.Millisecond
	h := newRealHub(t, 64, time.Minute)
	s, stop := listenServe(t, cfg, h)
	url := "ws://" + s.Addr().String() + "/v1/stream"
	mustDial(t, url) // never reads again, so never answers the close frame
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if d := time.Since(start); d > cfg.WriteTimeout+time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if s.Stats().Active != 0 {
		t.Fatalf("stats %+v", s.Stats())
	}
}

func TestBindFailure(t *testing.T) {
	before := runtime.NumGoroutine()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.Addr = ln.Addr().String()
	s, err := NewServer(newRealHub(t, 4, time.Minute), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err == nil {
		t.Fatal("Listen on occupied port succeeded")
	}
	if err := s.Serve(context.Background()); err == nil {
		t.Fatal("Serve without Listen succeeded")
	}
	settle(t, before)
}

func writeCert(t *testing.T, dir string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func TestTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, pool := writeCert(t, dir)
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.TLSCertFile, cfg.TLSKeyFile = certFile, keyFile
	h := newRealHub(t, 64, time.Minute)
	s, stop := listenServe(t, cfg, h)
	o := dialOpts(testOrigin, Protocol)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	defer tr.CloseIdleConnections()
	o.HTTPClient = &http.Client{Transport: tr}
	c, _, err := dialWS(t, "wss://"+s.Addr().String()+"/v1/stream", o)
	if err != nil {
		t.Fatal(err)
	}
	wantHello(t, readMsg(t, c), StateWaiting)
	go func() { _, _, _ = readUntilClose(c, 5*time.Second) }()
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	// Load failures name the file but never its content.
	badKey := filepath.Join(dir, "bad.pem")
	const secret = "-----BEGIN PRIVATE KEY-----\nU0VDUkVUS0VZTUFURVJJQUw=\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(badKey, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kf := range []string{badKey, filepath.Join(dir, "missing.pem")} {
		cfg.TLSKeyFile = kf
		s, err := NewServer(h, cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = s.Listen()
		if err == nil {
			t.Fatalf("Listen with key %s succeeded", kf)
		}
		if !strings.Contains(err.Error(), kf) || strings.Contains(err.Error(), "U0VDUkVU") {
			t.Fatalf("error %q", err)
		}
	}
}

func TestConnectionStorm(t *testing.T) {
	n := 1000
	if testing.Short() {
		n = 200
	}
	before := runtime.NumGoroutine()
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	h := newRealHub(t, 64, time.Minute)
	s, stop := listenServe(t, cfg, h)
	url := "ws://" + s.Addr().String() + "/v1/stream"
	for range n {
		c, _, err := dialWS(t, url, dialOpts(testOrigin, Protocol))
		if err != nil {
			t.Fatal(err)
		}
		wantHello(t, readMsg(t, c), StateWaiting)
		_ = c.CloseNow()
	}
	waitFor(t, "drain", func() bool { return s.Stats().Active == 0 && h.Stats().Clients == 0 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	settle(t, before)
}

func TestNewServerValidation(t *testing.T) {
	h := newRealHub(t, 4, time.Minute)
	var logs *observer.ObservedLogs
	if _, err := NewServer(h, baseConfig(&logs)); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if _, err := NewServer(nil, baseConfig(&logs)); err == nil {
		t.Error("nil hub accepted")
	}
	cases := map[string]func(*ServerConfig){
		"non-loopback no token": func(c *ServerConfig) { c.Loopback = false },
		"bad token":             func(c *ServerConfig) { c.Token = "short" },
		"empty origins":         func(c *ServerConfig) { c.Origins = nil },
		"bad origin":            func(c *ServerConfig) { c.Origins = []string{testOrigin, "null"} },
		"max clients 0":         func(c *ServerConfig) { c.MaxClients = 0 },
		"max clients 65":        func(c *ServerConfig) { c.MaxClients = 65 },
		"write timeout 0":       func(c *ServerConfig) { c.WriteTimeout = 0 },
		"tls cert only":         func(c *ServerConfig) { c.TLSCertFile = "c.pem" },
		"tls key only":          func(c *ServerConfig) { c.TLSKeyFile = "k.pem" },
		"empty addr":            func(c *ServerConfig) { c.Addr = "" },
	}
	for name, mod := range cases {
		cfg := baseConfig(&logs)
		mod(&cfg)
		_, err := NewServer(h, cfg)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if cfg.Token != "" && strings.Contains(err.Error(), cfg.Token) {
			t.Errorf("%s: error leaks token", name)
		}
	}
	cfg := baseConfig(&logs)
	cfg.Loopback, cfg.Token, cfg.MaxClients, cfg.Log = false, testToken, 64, nil
	if _, err := NewServer(h, cfg); err != nil {
		t.Fatalf("non-loopback with token: %v", err)
	}
}

func TestOriginPatterns(t *testing.T) {
	got := originPatterns([]string{"https://a.com", "http://[::1]:5173", "http://b.com"})
	want := []string{"https://a.com", "https://a.com:443", `http://\[::1\]:5173`, "http://b.com", "http://b.com:80"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("patterns %q, want %q", got, want)
	}
}

func TestRequestBodyRejected(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{"/healthz", "/v1/stream", "/nope"} {
		req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, strings.NewReader("x"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !resp.Close {
			t.Errorf("%s with body: %d close=%v, want 400 + Connection: close", path, resp.StatusCode, resp.Close)
		}
	}
	// Chunked body.
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/healthz", io.MultiReader(strings.NewReader("x")))
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("chunked body: %d, want 400", resp.StatusCode)
	}
}

// rawConn opens a TCP connection to addr and closes it when the test ends.
func rawConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func healthz(addr string, timeout time.Duration) (int, error) {
	cl := &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cl.Get("http://" + addr + "/healthz")
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// B1: complete requests that withhold a declared body must not pin listener slots.
func TestWithheldBodyDoesNotPinListener(t *testing.T) {
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.MaxClients = 1
	s, _ := listenServe(t, cfg, newRealHub(t, 4, time.Minute))
	addr := s.Addr().String()
	for range cfg.MaxClients + listenerSlack {
		c := rawConn(t, addr)
		if _, err := fmt.Fprintf(c, "GET /healthz HTTP/1.1\r\nHost: %s\r\nContent-Length: 1\r\n\r\n", addr); err != nil {
			t.Fatal(err)
		}
	}
	if code, err := healthz(addr, 2*time.Second); err != nil || code != 200 {
		t.Fatalf("healthz behind withheld-body conns: %d %v", code, err)
	}
}

func TestWebSocketOutlivesHTTPTimeouts(t *testing.T) {
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.readTimeout, cfg.httpWriteTimeout, cfg.readHeaderTimeout = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond
	h := newRealHub(t, 64, time.Minute)
	s, _ := listenServe(t, cfg, h)
	c := mustDial(t, "ws://"+s.Addr().String()+"/v1/stream")
	time.Sleep(900 * time.Millisecond)
	h.Publish(frame(1, 10, 20))
	wantStatus(t, readMsg(t, c), StateLive, ReasonFirstFrame)
	wantSample(t, readMsg(t, c), 1, 0)
}

func TestHeaderLimits(t *testing.T) {
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.readHeaderTimeout = 200 * time.Millisecond
	s, _ := listenServe(t, cfg, newRealHub(t, 4, time.Minute))
	addr := s.Addr().String()

	c := rawConn(t, addr)
	if _, err := fmt.Fprintf(c, "GET /healthz HTTP/1.1\r\nHost: %s\r\nX-Big: %s\r\n\r\n", addr, strings.Repeat("a", 16<<10)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("big header: %v %v, want 431", resp, err)
	}

	slow := rawConn(t, addr)
	if _, err := io.WriteString(slow, "GET /healthz HTTP/1.1\r\nHost: "); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = slow.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.Copy(io.Discard, slow) // returns when the server closes the connection
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("slow header conn closed after %v", d)
	}
}

func TestShutdownDeadlineReturnsError(t *testing.T) {
	var logs *observer.ObservedLogs
	cfg := baseConfig(&logs)
	cfg.WriteTimeout = 200 * time.Millisecond
	cfg.readHeaderTimeout = time.Minute
	s, stop := listenServe(t, cfg, newRealHub(t, 4, time.Minute))
	c := rawConn(t, s.Addr().String())
	if _, err := io.WriteString(c, "GET /healthz HTTP/1.1\r\n"); err != nil { // active request, never completed
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	err := stop()
	if err == nil || !strings.Contains(err.Error(), "shutdown deadline exceeded") {
		t.Fatalf("Serve = %v, want shutdown deadline error", err)
	}
	if d := time.Since(start); d > cfg.WriteTimeout+1500*time.Millisecond {
		t.Fatalf("shutdown took %v", d)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("stalled connection still open after shutdown")
	}
}

// wsHandshake performs a raw client handshake and returns the upgraded connection.
func wsHandshake(t *testing.T, addr string) net.Conn {
	t.Helper()
	c := rawConn(t, addr)
	_, err := fmt.Fprintf(c, "GET /v1/stream HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: %s\r\n\r\n", addr, testOrigin, Protocol)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: %v %v", resp, err)
	}
	return c
}

// N2: a client that floods data and never reads or answers the close frame is released within
// WriteTimeout (+1s slack).
func TestCloseBoundedWhileClientFloods(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.WriteTimeout = 200 * time.Millisecond })
	c := wsHandshake(t, strings.TrimPrefix(e.ts.URL, "http://"))
	waitFor(t, "registered", func() bool { return e.s.Stats().Active == 1 })
	// Masked text frames with a zero mask key, sent until the server drops the connection.
	f := append([]byte{0x81, 0x80 | 4, 0, 0, 0, 0}, "data"...)
	go func() {
		for {
			if _, err := c.Write(f); err != nil {
				return
			}
		}
	}()
	start := time.Now()
	waitFor(t, "flooding client release", func() bool { return e.s.Stats().Active == 0 })
	if d := time.Since(start); d > 200*time.Millisecond+time.Second {
		t.Fatalf("released after %v", d)
	}
}

func TestOriginEqualToHostRejected(t *testing.T) {
	e := newEnv(t, func(c *ServerConfig) { c.Loopback = false; c.Token = testToken })
	// The library would accept Origin == Host on its own; the pre-upgrade check must not.
	_, resp, err := dialWS(t, e.url, dialOpts(e.ts.URL, Protocol, "gt7.token."+testToken))
	if err == nil || status(resp) != http.StatusForbidden {
		t.Fatalf("same-host origin: %d %v, want 403", status(resp), err)
	}
	for _, o := range []string{testOrigin + "," + testOrigin, testOrigin + ", " + testOrigin} {
		resp, _ := raw(t, e.ts.URL, http.MethodGet, "/v1/stream", "", handshake(o, Protocol, "gt7.token."+testToken), true)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("comma origin %q: %d, want 403", o, resp.StatusCode)
		}
	}
	h := handshake(testOrigin, Protocol, "gt7.token."+testToken)
	h.Add("Origin", testOrigin)
	if resp, _ := raw(t, e.ts.URL, http.MethodGet, "/v1/stream", "", h, true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("two origins: %d", resp.StatusCode)
	}
}
