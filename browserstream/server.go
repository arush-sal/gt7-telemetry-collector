package browserstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/net/netutil"
)

// Close codes and reasons sent by the server (ADR 0002).
const (
	closeSlowConsumer websocket.StatusCode = 4000
	reasonShutdown                         = "collector_shutdown"
	reasonSlow                             = "slow_consumer"
	reasonReadOnly                         = "read_only_stream"
	readLimit                              = 1024
	maxClientsLimit                        = 64
	listenerSlack                          = 16
)

// ServerConfig configures a Server. See ADR 0003.
type ServerConfig struct {
	Addr                    string        // bind address, already validated by ParseListen
	Loopback                bool          // enables the Host header check
	Origins                 []string      // allowlist, normalized by NewServer; non-empty
	Token                   string        // "" = no token; else validated with ValidateToken
	MaxClients              int           // 1..64
	WriteTimeout            time.Duration // per-write deadline and close-handshake bound (>0)
	TLSCertFile, TLSKeyFile string        // both or neither
	Log                     *zap.Logger   // nil -> no-op

	pingInterval, pingTimeout time.Duration // tests only; zero -> 10s / 5s
	// tests only; zero -> 10s / 10s / 3s (http.Server ReadTimeout, WriteTimeout, ReadHeaderTimeout)
	readTimeout, httpWriteTimeout, readHeaderTimeout time.Duration
}

// ServerStats is a snapshot of the server counters.
type ServerStats struct {
	RejectedOrigin, RejectedToken, RejectedSubprotocol, RejectedCapacity, RejectedHost, RejectedMethod uint64
	Active                                                                                             int
}

// Server is the read-only WebSocket listener in front of a Hub.
type Server struct {
	hub      *Hub
	cfg      ServerConfig
	log      *zap.Logger
	origins  map[string]bool
	patterns []string
	token    [32]byte // sha256 of cfg.Token

	baseCtx    context.Context // cancelled when Serve starts shutting down
	cancelBase context.CancelFunc

	mu       sync.Mutex
	active   int
	closing  bool
	handlers sync.WaitGroup // streaming handlers; Add only under mu while !closing

	ln  net.Listener
	srv *http.Server

	rejOrigin, rejToken, rejProto, rejCap, rejHost, rejMethod atomic.Uint64
}

// NewServer validates cfg and returns a server for h.
func NewServer(h *Hub, cfg ServerConfig) (*Server, error) {
	switch {
	case h == nil:
		return nil, errors.New("browserstream: hub is required")
	case cfg.Addr == "":
		return nil, errors.New("browserstream: listen address is required")
	case len(cfg.Origins) == 0:
		return nil, errors.New("browserstream: at least one allowed origin is required")
	case cfg.MaxClients < 1 || cfg.MaxClients > maxClientsLimit:
		return nil, fmt.Errorf("browserstream: max clients must be 1..%d", maxClientsLimit)
	case cfg.WriteTimeout <= 0:
		return nil, errors.New("browserstream: write timeout must be > 0")
	case (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == ""):
		return nil, errors.New("browserstream: TLS cert and key must be set together")
	case !cfg.Loopback && cfg.Token == "":
		return nil, errors.New("browserstream: a token is required for a non-loopback listen address")
	}
	if cfg.Token != "" {
		if err := ValidateToken(cfg.Token); err != nil {
			return nil, fmt.Errorf("browserstream: %w", err)
		}
	}
	s := &Server{hub: h, cfg: cfg, log: cfg.Log, origins: map[string]bool{}, token: sha256.Sum256([]byte(cfg.Token))}
	norm := make([]string, 0, len(cfg.Origins))
	for _, o := range cfg.Origins {
		n, err := NormalizeOrigin(o)
		if err != nil {
			return nil, fmt.Errorf("browserstream: origin %q: %w", o, err)
		}
		s.origins[n] = true
		norm = append(norm, n)
	}
	s.cfg.Origins = norm
	s.patterns = originPatterns(norm)
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.cfg.pingInterval <= 0 {
		s.cfg.pingInterval = 10 * time.Second
	}
	if s.cfg.pingTimeout <= 0 {
		s.cfg.pingTimeout = 5 * time.Second
	}
	if s.cfg.readTimeout <= 0 {
		s.cfg.readTimeout = 10 * time.Second
	}
	if s.cfg.httpWriteTimeout <= 0 {
		s.cfg.httpWriteTimeout = 10 * time.Second
	}
	if s.cfg.readHeaderTimeout <= 0 {
		s.cfg.readHeaderTimeout = 3 * time.Second
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	return s, nil
}

// Stats returns a snapshot of the server counters.
func (s *Server) Stats() ServerStats {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	return ServerStats{
		RejectedOrigin: s.rejOrigin.Load(), RejectedToken: s.rejToken.Load(), RejectedSubprotocol: s.rejProto.Load(),
		RejectedCapacity: s.rejCap.Load(), RejectedHost: s.rejHost.Load(), RejectedMethod: s.rejMethod.Load(),
		Active: active,
	}
}

// Handler returns the HTTP handler (exposed for httptest).
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

// Listen loads the TLS key pair (if configured) and binds the address. Call before Serve.
func (s *Server) Listen() error {
	var tcfg *tls.Config
	if s.cfg.TLSCertFile != "" {
		// The tls errors name PEM block types at most, never key material.
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("browserstream: load TLS cert %q / key %q: %w", s.cfg.TLSCertFile, s.cfg.TLSKeyFile, err)
		}
		tcfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("browserstream: listen: %w", err)
	}
	ln = netutil.LimitListener(ln, s.cfg.MaxClients+listenerSlack)
	if tcfg != nil {
		ln = tls.NewListener(ln, tcfg)
	}
	s.ln = ln
	return nil
}

// Addr returns the bound address (nil before Listen).
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve serves until ctx is done, then stops accepting, closes every client with 1001
// collector_shutdown (each bounded by WriteTimeout) and waits for the handlers. It returns nil
// after a clean ctx cancellation, or an error when the serve loop failed or the shutdown deadline
// (WriteTimeout + 1s) passed, in which case the remaining connections are force-closed.
//
// net/http clears the Server's read/write deadlines on Hijack, so ReadTimeout/WriteTimeout bound
// only plain requests and handshakes, never established WebSockets.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("browserstream: Serve called before a successful Listen")
	}
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: s.cfg.readHeaderTimeout,
		ReadTimeout:       s.cfg.readTimeout,
		WriteTimeout:      s.cfg.httpWriteTimeout,
		MaxHeaderBytes:    8 << 10,
		IdleTimeout:       30 * time.Second,
	}
	// TLS handshake and similar noise goes to DEBUG; it never contains request headers.
	s.srv.ErrorLog, _ = zap.NewStdLogAt(s.log.Named("http"), zapcore.DebugLevel)
	errc := make(chan error, 1)
	go func() { errc <- s.srv.Serve(s.ln) }()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}

	deadline, cancel := context.WithTimeout(context.Background(), s.cfg.WriteTimeout+time.Second)
	defer cancel()
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.cancelBase()
	shutErr := make(chan error, 1)
	go func() { shutErr <- s.srv.Shutdown(deadline) }() // closes the listener first
	done := make(chan struct{})
	go func() { s.handlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline.Done():
	}
	err := <-shutErr
	if deadline.Err() != nil {
		_ = s.srv.Close()
		s.log.Warn("browser stream shutdown deadline exceeded; connections force-closed")
		return errors.Join(serveErr, errors.New("browserstream: shutdown deadline exceeded"))
	}
	if err != nil && serveErr == nil {
		serveErr = err
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return serveErr
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request, ctr *atomic.Uint64, code int, reason string) {
	if ctr != nil {
		ctr.Add(1)
	}
	s.log.Debug("browser stream request rejected", zap.String("remote", r.RemoteAddr), zap.String("reason", reason))
	body := http.StatusText(code)
	if code == http.StatusUnauthorized {
		body = "unauthorized"
	}
	http.Error(w, body, code)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Loopback && !loopbackHost(r.Host) {
		s.reject(w, r, &s.rejHost, http.StatusMisdirectedRequest, "host")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		// net/http drains a small unread body after the handler returns; a client that withholds it
		// would pin a LimitListener slot until ReadTimeout. Expire the read deadline so the drain
		// fails at once, and close the connection after the reply.
		w.Header().Set("Connection", "close")
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		s.reject(w, r, nil, http.StatusBadRequest, "body")
		return
	}
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case "/v1/stream":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			s.reject(w, r, &s.rejMethod, http.StatusMethodNotAllowed, "method")
			return
		}
		s.serveStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Values("Origin"); len(o) != 1 || !s.allowedOrigin(o[0]) {
		s.reject(w, r, &s.rejOrigin, http.StatusForbidden, "origin")
		return
	}
	tok, hasTok, err := parseSubprotocols(r.Header)
	if err != nil {
		s.reject(w, r, &s.rejProto, http.StatusBadRequest, "subprotocol")
		return
	}
	if s.cfg.Token != "" {
		sum := sha256.Sum256([]byte(tok))
		if !hasTok || subtle.ConstantTimeCompare(sum[:], s.token[:]) != 1 {
			s.reject(w, r, &s.rejToken, http.StatusUnauthorized, "token")
			return
		}
	}
	if !s.reserve() {
		s.reject(w, r, &s.rejCap, http.StatusServiceUnavailable, "capacity")
		return
	}
	defer s.release()
	c, ok := s.hub.register()
	if !ok {
		s.reject(w, r, nil, http.StatusServiceUnavailable, "hub_closed")
		return
	}
	hw := &hijackRecorder{ResponseWriter: w}
	conn, err := websocket.Accept(hw, r, &websocket.AcceptOptions{
		Subprotocols:    []string{Protocol},
		OriginPatterns:  s.patterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.hub.unregister(c)
		s.log.Debug("browser stream request rejected", zap.String("remote", r.RemoteAddr), zap.String("reason", "handshake"))
		return
	}
	s.runConn(conn, hw.conn, c)
}

// hijackRecorder keeps the hijacked net.Conn so closes can be bounded with a socket deadline.
type hijackRecorder struct {
	http.ResponseWriter
	conn net.Conn
}

// Hijack implements http.Hijacker for websocket.Accept and records the connection.
func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	h.conn = c
	return c, brw, err
}

// allowedOrigin normalizes a request Origin and compares it exactly with the allowlist.
func (s *Server) allowedOrigin(o string) bool {
	n, err := NormalizeOrigin(o)
	return err == nil && s.origins[n]
}

// reserve takes a client slot; it fails at capacity or once shutdown began.
func (s *Server) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.active >= s.cfg.MaxClients {
		return false
	}
	s.active++
	s.handlers.Add(1)
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	s.handlers.Done()
}

// runConn streams to one accepted client until it leaves, sends data, falls behind, stops
// answering pings or the server shuts down, then closes the connection within WriteTimeout.
//
// The reader and pinger end the stream with hub.unregister, never by cancelling its context: the
// hub counts a missed write deadline as slow only while the context is live, and the library drops
// the socket on that deadline, which the reader would otherwise race. Only shutdown cancels.
//
// A local reader loop replaces conn.CloseRead: CloseRead closes from inside its own goroutine and
// then waits up to 15s for that goroutine, which would hold the slot long after the 1008 close.
func (s *Server) runConn(conn *websocket.Conn, nc net.Conn, c *client) {
	conn.SetReadLimit(readLimit)
	leave := func() { s.hub.unregister(c) }
	var wg sync.WaitGroup
	defer wg.Wait() // every exit path closes conn, which ends both goroutines

	var gotData atomic.Bool
	wg.Go(func() { // reader: processes ping/pong/close frames; any data message ends the stream
		defer leave()
		for {
			_, r, err := conn.Reader(context.Background())
			if err != nil {
				return
			}
			gotData.Store(true)
			// Drain first so an oversized message gets the library's 1009 before our 1008.
			_, err = io.Copy(io.Discard, r)
			leave()
			if err != nil {
				return
			}
		}
	})
	wg.Go(func() { // pinger: a peer that stops answering is closed within interval+timeout
		t := time.NewTicker(s.cfg.pingInterval)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-s.baseCtx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(s.baseCtx, s.cfg.pingTimeout)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					leave()
					return
				}
			}
		}
	})

	err := s.hub.stream(s.baseCtx, c, func(ctx context.Context, b []byte) error {
		return conn.Write(ctx, websocket.MessageText, b)
	}, s.cfg.WriteTimeout)

	var code websocket.StatusCode
	var reason string
	switch {
	case gotData.Load():
		code, reason = websocket.StatusPolicyViolation, reasonReadOnly
	case s.baseCtx.Err() != nil:
		code, reason = websocket.StatusGoingAway, reasonShutdown
	case errors.Is(err, context.DeadlineExceeded):
		code, reason = closeSlowConsumer, reasonSlow // best effort: the library already dropped the socket
	default: // client gone or not answering pings
		_ = conn.CloseNow()
		return
	}
	// The socket deadline bounds every raw read and write in the library's close path (its own
	// internal timeouts are 5s and 15s).
	_ = nc.SetDeadline(time.Now().Add(s.cfg.WriteTimeout))
	_ = conn.Close(code, reason)
}
