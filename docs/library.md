# Library

The module is `github.com/arush-sal/gt7-telemetry-collector` (Go 1.27.4+).
Full API documentation is on
[pkg.go.dev](https://pkg.go.dev/github.com/arush-sal/gt7-telemetry-collector).

## Packages

| Package | Purpose |
| --- | --- |
| `protocol` | Salsa20 decryption, validation, GT7 packet decoding, deduplication, and heartbeat payload. |
| `telemetry` | Maps GT7 data and capture metadata to canonical protobuf frames. |
| `ingest` | UDP receiver and raw/decrypt/dedup/decode/map/publish pipeline. |
| `transport` | Buffered gRPC, Unix socket, and HTTP(S) publisher with drop-oldest handling and reconnect/backoff. |
| `rawsink` | Asynchronous, afero-backed raw packet dumps. |
| `replay` | Opens pcap/pcapng captures and feeds UDP payloads to a handler with scaled pacing. |
| `gt7data` | Car names, drivetrains, and reference-data lookups. |
| `gen/gt7/telemetry/v1` | Generated protobuf messages and gRPC service bindings. |
| `cmd/gt7-collector` | Standalone collector command. |
| `gt7data/internal/gen` | Internal command that generates car reference data. |

## Receive and print frames

Supply any publisher implementing `Publish(*telemetryv1.TelemetryFrame)`. This
complete example prints canonical frames locally; it does not connect to RTPS.
Printing runs inline and can block, so use a buffered publisher such as
`transport.Publisher` for sustained collection.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/protocol"
)

type printer struct{}

func (printer) Publish(frame *telemetryv1.TelemetryFrame) {
	fmt.Println(frame)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pipeline := ingest.NewPipeline(nil, printer{}) // nil disables raw dumping
	receiver, err := ingest.NewReceiver(
		protocol.RecvPort, protocol.SendPort, "192.168.1.50",
		time.Second, pipeline.Handle,
	)
	if err != nil { // the constructor binds the socket, so bind errors fail immediately
		log.Fatal(err)
	}
	if err := receiver.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
```

## Publish to an RTPS

```go
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/protocol"
	"github.com/arush-sal/gt7-telemetry-collector/transport"
	"go.uber.org/zap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pub, err := transport.New("unix:///run/garage/ingest.sock", 1024, zap.NewNop(),
		transport.WithToken("")) // empty token: no authentication
	if err != nil {
		log.Fatal(err)
	}
	go func() { _ = pub.Run(ctx) }()

	pipeline := ingest.NewPipeline(nil, pub)
	recv, err := ingest.NewReceiver(protocol.RecvPort, protocol.SendPort,
		"192.168.1.50", time.Second, pipeline.Handle)
	if err != nil {
		log.Fatal(err)
	}
	defer recv.Close()
	_ = recv.Run(ctx)
}
```

## Decode a single packet

For a single encrypted payload, use `protocol.Decrypt` followed by
`protocol.Decode`:

```go
package main

import "github.com/arush-sal/gt7-telemetry-collector/protocol"

func decodePacket(enc []byte) (protocol.GT7DData, error) {
	plaintext, err := protocol.Decrypt(enc)
	if err != nil {
		return protocol.GT7DData{}, err
	}
	return protocol.Decode(plaintext)
}

func main() { _, _ = decodePacket(nil) }
```

`Decrypt` returns `protocol.ErrPacketLength` for an unrecognized length and
`protocol.ErrBadMagic` when the decrypted magic is wrong; test with `errors.Is`.
The pipeline already performs both steps plus deduplication and canonical
mapping, so do not decrypt packets before passing them to `pipeline.Handle`.

## Replay a capture

Open a pcap or pcapng capture with `replay.Open(path)` and close the returned
`*replay.Source` when finished:

```go
func (s *Source) Play(ctx context.Context, recvPort uint16, speed float64,
	handle func([]byte, time.Time)) error
```

```go
package main

import (
	"context"
	"log"
	"math"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/protocol"
	"github.com/arush-sal/gt7-telemetry-collector/replay"
)

type counter struct{ n int }

func (c *counter) Publish(*telemetryv1.TelemetryFrame) { c.n++ }

func main() {
	source, err := replay.Open("rawdump/raw-1700000000.pcap")
	if err != nil {
		log.Fatal(err)
	}
	defer source.Close()

	pub := &counter{}
	pipeline := ingest.NewPipeline(nil, pub)
	// 1.0 replays at the original cadence, 2.0 doubles the speed.
	if err := source.Play(context.Background(), uint16(protocol.RecvPort),
		math.Inf(1), pipeline.Handle); err != nil {
		log.Fatal(err)
	}
	log.Printf("frames: %d, counters: %+v", pub.n, pipeline.Counters())
}
```

`Play` skips traffic not addressed to `recvPort` (DNS, heartbeats to the send
port, other UDP), so counters match a live run. `math.Inf(1)` plays at maximum
speed.

## Reference data

```go
name, ok := gt7data.CarName(carID)        // ("", false) when unknown
drivetrain, ok := gt7data.Drivetrain(carID) // FF, FR, MR, RR or 4WD
```

The canonical frame already carries a resolved `car_name` ("Unknown" when
unmapped).
