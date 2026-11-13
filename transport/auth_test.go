package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authServer records metadata after receiving an actual telemetry frame.
type authServer struct {
	telemetryv1.UnimplementedTelemetryServer
	headers   chan []string
	failFirst bool
	attempts  atomic.Int32
}

func (s *authServer) Ingest(stream telemetryv1.Telemetry_IngestServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.headers <- md.Get(AuthMetadataKey)
	if s.failFirst && s.attempts.Add(1) == 1 {
		return status.Error(codes.Unavailable, "forced first stream failure")
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestBearerConstants(t *testing.T) {
	if AuthHeader != "Authorization" || AuthMetadataKey != "authorization" || BearerPrefix != "Bearer " {
		t.Fatal("unexpected bearer wire constants")
	}
}

func TestBearerTransport(t *testing.T) {
	for _, protocol := range []string{"grpc", "bare", "http"} {
		for _, mode := range []string{"omitted", "empty", "token"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				headers := make(chan []string, 1)
				var target string
				if protocol != "http" {
					lis, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					server := grpc.NewServer()
					telemetryv1.RegisterTelemetryServer(server, &authServer{headers: headers})
					go func() { _ = server.Serve(lis) }()
					t.Cleanup(server.Stop)
					target = lis.Addr().String()
					if protocol == "grpc" {
						target = "grpc://" + target
					}
				} else {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if _, err := NewNDJSONDecoder(r.Body).Next(); err != nil {
							return
						}
						headers <- r.Header.Values(AuthHeader)
					}))
					t.Cleanup(server.Close)
					target = server.URL
				}
				var opts []Option
				var want []string
				switch mode {
				case "empty":
					opts = append(opts, WithToken(""))
				case "token":
					opts = append(opts, WithToken("test-secret"))
					want = []string{"Bearer test-secret"}
				}
				p := startPublisher(t, target, opts...)
				p.Publish(&telemetryv1.TelemetryFrame{Sequence: 1})
				select {
				case got := <-headers:
					if !reflect.DeepEqual(got, want) {
						t.Errorf("authorization = %v, want %v", got, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("no request received")
				}
			})
		}
	}
}

func TestTokenErrorsAreRedacted(t *testing.T) {
	const token = "secret-value"
	p, err := New("localhost:50051", 1, nil, WithToken(token))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("receiver echoed " + token)
	p.dial = func(context.Context) (stream, error) { return nil, cause }
	_, err = p.streamOnce(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("token leaked in stream error")
	}
	if !errors.Is(err, cause) {
		t.Fatal("redaction lost error identity")
	}
	for _, target := range []string{"localhost:50051", "http://localhost/ingest"} {
		_, err := New(target, 1, nil, WithToken(token+"\n"))
		if err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("unsafe token validation error")
		}
	}
	if _, err := New("unix:///tmp/ingest.sock", 1, nil, WithToken(token+"\n")); err != nil {
		t.Fatal("Unix token was not ignored")
	}
}

func TestCredentialTargetsRejected(t *testing.T) {
	for _, target := range []string{
		"http://user:secret@localhost/ingest", "https://user:secret@localhost/ingest",
		"http://localhost/ingest?token=secret", "https://localhost/ingest?",
	} {
		_, err := New(target, 1, nil)
		if err == nil || !strings.Contains(err.Error(), "WithToken") || strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "secret") {
			t.Errorf("credential target rejection = %v", err)
		}
	}
}

func TestConstructorErrorsHideTarget(t *testing.T) {
	for _, target := range []string{"secret", "grpc://secret", "http://secret/%zz", "secret://localhost", "http://user:secret@localhost/%zz"} {
		_, err := New(target, 1, nil)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe constructor error: %v", err)
		}
	}
}

func TestTokenGrammar(t *testing.T) {
	for _, target := range []string{"localhost:50051", "grpc://localhost:50051", "http://localhost/ingest", "https://localhost/ingest"} {
		for _, token := range []string{" secret", "secret ", "sec ret", "secret\t", "secret\n", "secret:bad", "secret,", "secret=bad", "=", "secreté"} {
			_, err := New(target, 1, nil, WithToken(token))
			if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "secret") {
				t.Errorf("unsafe token validation: %v", err)
			}
		}
		if _, err := New(target, 1, nil, WithToken("aZ09-._~+/==")); err != nil {
			t.Errorf("valid b64token rejected: %v", err)
		}
	}
}

func TestGRPCReconnectAuthorization(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	headers := make(chan []string, 2)
	server := grpc.NewServer()
	telemetryv1.RegisterTelemetryServer(server, &authServer{headers: headers, failFirst: true})
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	p := startPublisher(t, "grpc://"+lis.Addr().String(), WithToken("reconnect-token"))
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for received := 0; received < 2; {
		select {
		case got := <-headers:
			if !reflect.DeepEqual(got, []string{"Bearer reconnect-token"}) {
				t.Fatalf("stream %d authorization = %v", received+1, got)
			}
			received++
		case <-ticker.C:
			p.Publish(&telemetryv1.TelemetryFrame{Sequence: 1})
		case <-timeout.C:
			t.Fatal("second stream did not receive a frame after forced failure")
		}
	}
}
