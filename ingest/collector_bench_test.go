package ingest

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"github.com/arush-sal/gt7-telemetry-collector/protocol"
)

// nopPub is a Publisher that discards every frame, so the benchmark measures the pipeline alone.
type nopPub struct{}

// Publish discards f.
func (nopPub) Publish(*telemetryv1.TelemetryFrame) {}

// benchPackageIDOff is the plaintext offset of the little-endian uint32 PackageID (protocol's
// unexported offPackageID).
const benchPackageIDOff = 0x70

// BenchmarkPipelineHandle measures the full accepted path (decrypt -> dedup -> decode -> map ->
// publish) over the 8 format-A and 8 format-C fixtures, round-robin.
//
// Dedup rejects non-increasing PackageIDs, so replaying the fixtures verbatim would measure the
// stale-reject path. Salsa20 is a stream cipher (ciphertext = plaintext XOR keystream), and the
// keystream depends only on the cleartext IV at 0x40, not on the PackageID. So to give each
// iteration a fresh, strictly increasing PackageID we XOR the delta (old ^ new) into the four
// ciphertext bytes at the PackageID offset of a per-fixture working copy: no cipher or production
// code needed, and no per-iteration allocation. The 4-byte patch is inside the timed loop (a few ns).
func BenchmarkPipelineHandle(b *testing.B) {
	var pkts [][]byte
	var cur []uint32 // PackageID currently encoded in each working copy
	for _, pat := range []string{"packet_%02d.bin", "packet_c_%02d.bin"} {
		for i := range 8 {
			enc := mustFix(b, fmt.Sprintf(pat, i))
			dec, err := protocol.Decrypt(enc)
			if err != nil {
				b.Fatalf("decrypt fixture: %v", err)
			}
			pkts = append(pkts, enc)
			cur = append(cur, protocol.PeekPackageID(dec))
		}
	}

	p := NewPipeline(nil, nopPub{})
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
		b.Fatalf("published %d of %d: benchmark is not measuring the accepted path (%+v)", c.Published, b.N, c)
	}
}

// mustFix loads a fixture for a benchmark (readFix takes *testing.T, which a benchmark lacks).
func mustFix(b *testing.B, name string) []byte {
	b.Helper()
	enc, err := os.ReadFile(filepath.Join("..", "protocol", "testdata", name))
	if err != nil {
		b.Fatalf("read fixture %s: %v", name, err)
	}
	return enc
}
