package browserstream

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arush-sal/gt7-telemetry-collector/ingest"
	"github.com/arush-sal/gt7-telemetry-collector/protocol"
)

// benchHub returns a hub with n registered clients. When drain is true each client's queue is
// emptied by its own goroutine (stand-in for a writer); otherwise queues fill and Publish takes
// the drop-oldest path. The returned func stops the drainers and waits for them to exit, so
// sub-benchmarks never overlap.
func benchHub(b *testing.B, n int, drain bool) (*Hub, func()) {
	b.Helper()
	h, err := NewHub(HubConfig{SessionID: fixSession, ClientBuffer: 64, SourceTimeout: time.Second})
	if err != nil {
		b.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		c, _ := h.register()
		if drain {
			wg.Go(func() {
				for {
					select {
					case <-c.samples:
					case <-stop:
						return
					}
				}
			})
		}
	}
	return h, func() { close(stop); wg.Wait() }
}

func BenchmarkHubPublish(b *testing.B) {
	for _, n := range []int{0, 4, 64} {
		for _, drain := range []bool{false, true} {
			if n == 0 && drain {
				continue
			}
			mode := "full"
			if drain {
				mode = "draining"
			}
			b.Run(fmt.Sprintf("clients=%d/%s", n, mode), func(b *testing.B) {
				h, stop := benchHub(b, n, drain)
				defer stop()
				f := frame(1, 10, 20)
				b.ReportAllocs()
				for b.Loop() {
					h.Publish(f)
				}
			})
		}
	}
}

// benchPackageIDOff is the plaintext offset of the little-endian uint32 PackageID (mirrors
// ingest's benchmark; protocol's offset constant is unexported).
const benchPackageIDOff = 0x70

// BenchmarkPipelineWithHub mirrors ingest.BenchmarkPipelineHandle (same fixtures, same XOR
// PackageID patch so every iteration takes the accepted path) with the hub and 4 draining clients
// as the publisher. Compare against BenchmarkPipelineHandle: the hub must add 0 allocs/op.
func BenchmarkPipelineWithHub(b *testing.B) {
	var pkts [][]byte
	var cur []uint32
	for _, pat := range []string{"packet_%02d.bin", "packet_c_%02d.bin"} {
		for i := range 8 {
			enc, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", fmt.Sprintf(pat, i)))
			if err != nil {
				b.Fatal(err)
			}
			dec, err := protocol.Decrypt(enc)
			if err != nil {
				b.Fatalf("decrypt fixture: %v", err)
			}
			pkts = append(pkts, enc)
			cur = append(cur, protocol.PeekPackageID(dec))
		}
	}
	h, stop := benchHub(b, 4, true)
	defer stop()
	p := ingest.NewPipeline(nil, h)
	ts := time.Unix(0, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := i % len(pkts)
		id := uint32(i) + 1
		pkt := pkts[k]
		d := binary.LittleEndian.Uint32(pkt[benchPackageIDOff:]) ^ cur[k] ^ id
		binary.LittleEndian.PutUint32(pkt[benchPackageIDOff:], d)
		cur[k] = id
		p.Handle(pkt, ts)
	}
	b.StopTimer()
	if c := p.Counters(); c.Published != uint64(b.N) {
		b.Fatalf("published %d of %d: not measuring the accepted path (%+v)", c.Published, b.N, c)
	}
}
