package transport

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"go.uber.org/zap"
)

func TestTargets(t *testing.T) {
	for _, target := range []string{"localhost:50051", "grpc://localhost:50051", "unix:///tmp/consumer.sock", "http://localhost:8080/ingest", "https://localhost/ingest"} {
		if _, err := New(target, 8, zap.NewNop()); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"ftp://localhost:21", "unix://relative.sock", "unix:relative.sock", "unix:///", "grpc://", "http://", ""} {
		if _, err := New(target, 8, zap.NewNop()); err == nil {
			t.Errorf("%s accepted", target)
		}
	}
	if _, err := New("localhost:50051", 0, nil); err == nil {
		t.Fatal("zero buffer accepted")
	}
}

func startPublisher(t *testing.T, target string, opts ...Option) *Publisher {
	t.Helper()
	p, err := New(target, 8, zap.NewNop(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("publisher failed to stop")
		}
	})
	return p
}

func receiveThree(t *testing.T, frames <-chan *telemetryv1.TelemetryFrame) {
	t.Helper()
	for i := uint64(1); i <= 3; i++ {
		select {
		case f := <-frames:
			if f.Sequence != i {
				t.Fatalf("sequence %d, want %d", f.Sequence, i)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}
}

func TestUnixPublisher(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path, err := filepath.Abs(filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	frames := make(chan *telemetryv1.TelemetryFrame, 3)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			f, err := ReadFrame(c)
			if err != nil {
				return
			}
			frames <- f
		}
	}()
	p := startPublisher(t, "unix://"+path, WithToken("ignored-token"))
	for i := uint64(1); i <= 3; i++ {
		p.Publish(&telemetryv1.TelemetryFrame{Sequence: i})
	}
	receiveThree(t, frames)
}

func TestHTTPPublisher(t *testing.T) {
	for _, failOnce := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "reconnect"}[failOnce], func(t *testing.T) {
			frames := make(chan *telemetryv1.TelemetryFrame, 3)
			var attempts atomic.Int32
			connected := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/ingest" || r.Header.Get("Content-Type") != "application/x-ndjson" {
					t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
				}
				if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
					t.Error(err)
					return
				}
				if r.Header.Get(AuthHeader) != BearerPrefix+"reconnect-token" {
					t.Error("missing bearer token on connection attempt")
				}
				n := attempts.Add(1)
				if failOnce && n == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					w.(http.Flusher).Flush()
					return
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case connected <- struct{}{}:
				case <-r.Context().Done():
					return
				}
				d := NewNDJSONDecoder(r.Body)
				for {
					f, err := d.Next()
					if err == io.EOF {
						return
					}
					if err != nil {
						return
					}
					frames <- f
				}
			}))
			t.Cleanup(srv.Close)
			p := startPublisher(t, srv.URL+"/ingest", WithToken("reconnect-token"))
			// Reconnection must happen even when no frames are queued.
			select {
			case <-connected:
			case <-time.After(5 * time.Second):
				t.Fatal("no successful connection")
			}
			for i := uint64(1); i <= 3; i++ {
				p.Publish(&telemetryv1.TelemetryFrame{Sequence: i})
			}
			receiveThree(t, frames)
		})
	}
}

func TestPlaintext(t *testing.T) {
	for _, tt := range []struct {
		target string
		want   bool
	}{
		{"localhost:50051", true},
		{"grpc://localhost:50051", true},
		{"GRPC://localhost:50051", true},
		{"http://localhost/ingest", true},
		{"HTTP://localhost/ingest", true},
		{"https://localhost/ingest", false},
		{"HTTPS://localhost/ingest", false},
		{"unix:///tmp/ingest.sock", false},
		{"unix:/tmp/ingest.sock", false},
		{"UNIX:/tmp/ingest.sock", false},
	} {
		t.Run(tt.target, func(t *testing.T) {
			got, err := Plaintext(tt.target)
			if err != nil || got != tt.want {
				t.Fatalf("Plaintext = %v, %v; want %v", got, err, tt.want)
			}
			if _, err := New(tt.target, 1, nil); err != nil {
				t.Fatalf("New rejected classified target: %v", err)
			}
		})
	}
	for _, target := range []string{"", "grpc://", "http://user:secret@localhost/", "http://localhost/?", "unix:relative", "ftp://localhost"} {
		if _, err := Plaintext(target); err == nil {
			t.Errorf("Plaintext accepted invalid target")
		}
	}
}
