package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

type stream interface {
	Send(*telemetryv1.TelemetryFrame) error
	Close() error
}

// New creates a buffered push publisher for a bare gRPC address, grpc://, unix:///, or HTTP(S) URL.
// gRPC uses plaintext; Unix sockets carry length-prefixed protobuf, and HTTP carries protojson NDJSON.
// WithToken enables bearer authentication except on Unix sockets, which rely on file permissions.
func New(target string, bufSize int, log *zap.Logger, opts ...Option) (*Publisher, error) {
	if bufSize <= 0 {
		return nil, fmt.Errorf("transport: buffer size must be positive")
	}
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}
	u, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	// Unix socket tokens are ignored and rely on socket permissions.
	if u.Scheme != "unix" && cfg.token != "" && !validToken(cfg.token) {
		return nil, fmt.Errorf("transport: token must be an RFC 6750 b64token")
	}
	var dial func(context.Context) (stream, error)
	switch u.Scheme {
	case "grpc":
		dial = grpcDial(u.Host, []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, cfg.token)
	case "unix":
		dial = func(ctx context.Context) (stream, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = c.Close() })
			return &unixStream{Conn: c, stop: stop}, nil
		}
	case "http", "https":
		dial = func(ctx context.Context) (stream, error) { return dialHTTP(ctx, u.String(), cfg.token) }
	}

	if log == nil {
		log = zap.NewNop()
	}
	return &Publisher{addr: target, token: cfg.token, dial: dial, ch: make(chan *telemetryv1.TelemetryFrame, bufSize), log: log}, nil
}

// Plaintext reports whether a valid target uses unencrypted TCP (gRPC or HTTP).
// Unix sockets return false because they rely on socket permissions.
func Plaintext(target string) (bool, error) {
	u, err := parseTarget(target)
	if err != nil {
		return false, err
	}
	return u.Scheme == "grpc" || u.Scheme == "http", nil
}

// parseTarget is shared by construction and transport classification. Its errors
// never include input text, which may contain credentials or other secrets.
func parseTarget(target string) (*url.URL, error) {
	if !strings.Contains(target, "://") && !strings.HasPrefix(strings.ToLower(target), "unix:") {
		if _, _, err := net.SplitHostPort(target); err != nil {
			return nil, fmt.Errorf("transport: invalid gRPC address")
		}
		return &url.URL{Scheme: "grpc", Host: target}, nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("transport: invalid target URL; use WithToken for credentials")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "grpc":
		if u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil, fmt.Errorf("transport: invalid gRPC URL; use WithToken for credentials")
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return nil, fmt.Errorf("transport: invalid gRPC address")
		}
	case "unix":
		if u.Host != "" || u.User != nil || !filepath.IsAbs(u.Path) || u.Path == "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil, fmt.Errorf("transport: unix URL requires an absolute socket path")
		}
	case "http", "https":
		if u.User != nil || u.RawQuery != "" || u.ForceQuery {
			return nil, fmt.Errorf("transport: HTTP URL credentials and query strings are forbidden; use WithToken")
		}
		if u.Host == "" || u.Fragment != "" {
			return nil, fmt.Errorf("transport: HTTP URL requires a host and no fragment")
		}
	default:
		return nil, fmt.Errorf("transport: unsupported scheme")
	}
	return u, nil
}

type grpcStream struct {
	telemetryv1.Telemetry_IngestClient
	conn *grpc.ClientConn
}

// Close releases the gRPC connection and its ingest stream.
func (s *grpcStream) Close() error { return s.conn.Close() }

func grpcDial(addr string, opts []grpc.DialOption, token string) func(context.Context) (stream, error) {
	return func(ctx context.Context) (stream, error) {
		c, err := grpc.NewClient(addr, opts...)
		if err != nil {
			return nil, err
		}
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, AuthMetadataKey, BearerPrefix+token)
		}
		s, err := telemetryv1.NewTelemetryClient(c).Ingest(ctx)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		return &grpcStream{Telemetry_IngestClient: s, conn: c}, nil
	}
}

type unixStream struct {
	net.Conn
	stop func() bool
}

// Send writes one length-prefixed protobuf frame.
func (s *unixStream) Send(f *telemetryv1.TelemetryFrame) error { return WriteFrame(s.Conn, f) }

// Close releases the socket and stops its cancellation callback.
func (s *unixStream) Close() error { s.stop(); return s.Conn.Close() }

type httpStream struct {
	writer *io.PipeWriter
	cancel context.CancelFunc
	result chan error
}

func dialHTTP(ctx context.Context, target, token string) (stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, reader)
	if err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if token != "" {
		req.Header.Set(AuthHeader, BearerPrefix+token)
	}
	s := &httpStream{writer: writer, cancel: cancel, result: make(chan error, 1)}
	go func() {
		// Redirects are responses to this stream, not permission to replay it elsewhere.
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				err = fmt.Errorf("transport: HTTP status %s", resp.Status)
			} else {
				// A server may flush 2xx headers while it continues consuming the POST.
				// Keep the upload open until its response actually completes.
				_, err = io.Copy(io.Discard, resp.Body)
				if err == nil {
					err = io.EOF
				}
			}
			// A terminal response ends this POST. Cancel before closing its body so a
			// server waiting for the streaming request to finish cannot deadlock cleanup.
			cancel()
			_ = reader.CloseWithError(err)
			_ = resp.Body.Close()
		}
		_ = reader.CloseWithError(err)
		s.result <- err
	}()
	return s, nil
}

// Send writes one protojson frame and its terminating newline.
func (s *httpStream) Send(f *telemetryv1.TelemetryFrame) error {
	b, err := protojson.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameSize {
		return fmt.Errorf("transport: line exceeds %d bytes", MaxFrameSize)
	}
	return writeAll(s.writer, append(b, '\n'))
}

// Close cancels the streaming POST and closes its request body.
func (s *httpStream) Close() error { s.cancel(); return s.writer.Close() }
