---
title: Transports
weight: 4
---

The collector dials the consumer (push model). `--rtps` selects the transport:

| Target | Wire format |
| --- | --- |
| `localhost:50051` or `grpc://localhost:50051` | Plaintext gRPC `Telemetry.Ingest` client stream. |
| `unix:///absolute/path.sock` | Unix stream socket; four-byte big-endian payload length followed by protobuf bytes. |
| `http://host/path` or `https://host/path` | One long-lived streaming POST to the URL path, `Content-Type: application/x-ndjson`; one protojson frame per line. |

Browsers cannot accept inbound connections, so the optional browser stream is
the reverse: the collector listens and the browser dials it. See
[Browser streaming]({{< relref "browser-streaming" >}}).

The schema is `gt7.telemetry.v1.TelemetryFrame`
([`proto/gt7/telemetry/v1/telemetry.proto`](https://github.com/arush-sal/gt7-telemetry-collector/blob/master/proto/gt7/telemetry/v1/telemetry.proto)).

## Target rules

- A bare `host:port` is treated as `grpc://host:port`. Schemes are
  case-insensitive.
- `grpc://` targets take only `host:port`: no path, query or fragment.
- `unix://` requires an absolute socket path and no host. The consumer listens;
  the collector connects.
- HTTP(S) targets must not contain URL userinfo or any query string (including an
  empty `?`), and need a host and no fragment. Supply credentials with
  `--rtps-token`, `INGEST_TOKEN`, or `transport.WithToken` instead.

## Authentication

Set `INGEST_TOKEN` or pass `--rtps-token` to send a bearer token.
HTTP(S) sends `Authorization: Bearer <token>` on the streaming POST. gRPC sends
`authorization: Bearer <token>` as per-RPC metadata on each `Ingest` stream.
Unix sockets ignore the token and rely on socket file permissions. Network
tokens must follow RFC 6750 `b64token` syntax: one or more ASCII letters, digits,
or `-._~+/`, followed by optional `=` padding. Whitespace is rejected.
See [Security]({{< relref "security" >}}).

## Delivery behavior

All transports share the bounded drop-oldest queue, reconnect with backoff, and
one warning per outage. Delivery is best effort, without acknowledgments, and
frames already dequeued when a send fails are not retried.

For HTTP(S): non-2xx responses trigger a reconnect, redirects are not followed,
and a completed HTTP response ends the stream. The consumer must read the
streaming request body incrementally. For Go HTTP/1 servers that respond before
consuming the body, call `http.NewResponseController(w).EnableFullDuplex()`
before writing the response.

## Framing limits

Each protobuf frame and each NDJSON line is capped at `transport.MaxFrameSize`
(4 MiB, excluding the length prefix or newline). `ReadFrame` rejects larger
lengths before allocating.

| Format | Framing | Encoding |
| --- | --- | --- |
| Unix socket | 4-byte big-endian length, then payload | protobuf |
| HTTP(S) | newline-delimited | protojson |

## Consumer examples

### Unix socket

For a connection accepted by a consumer:

```go
package consumer

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/arush-sal/gt7-telemetry-collector/transport"
)

func consume(conn net.Conn) error {
	defer conn.Close()
	for {
		frame, err := transport.ReadFrame(conn)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Println(frame.GetSequence())
	}
}
```

### HTTP NDJSON

```go
package consumer

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/arush-sal/gt7-telemetry-collector/transport"
)

func ingest(w http.ResponseWriter, r *http.Request) {
	// Needed when the handler responds before the request body is consumed.
	_ = http.NewResponseController(w).EnableFullDuplex()

	want := transport.BearerPrefix + os.Getenv("INGEST_TOKEN")
	got := r.Header.Get(transport.AuthHeader)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	dec := transport.NewNDJSONDecoder(r.Body)
	for {
		frame, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			http.Error(w, "invalid telemetry stream", http.StatusBadRequest)
			return
		}
		fmt.Println(frame.GetSequence())
	}
}
```

{{< callout type="info" >}}
The token is compared in constant time. Reject requests outright when no token is
configured if the listener is reachable from untrusted networks.
{{< /callout >}}

### gRPC

Implement `telemetryv1.TelemetryServer` from
`github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1` and read
frames from the `Ingest` client stream. Check the `authorization` metadata key
(`transport.AuthMetadataKey`) against `transport.BearerPrefix + token`.
