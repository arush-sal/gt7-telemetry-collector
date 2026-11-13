package transport

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"google.golang.org/protobuf/proto"
)

func TestFraming(t *testing.T) {
	want := &telemetryv1.TelemetryFrame{Sequence: 42}
	var b bytes.Buffer
	if err := WriteFrame(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&b)
	if err != nil || !proto.Equal(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ReadFrame(&b); err != io.EOF {
		t.Fatalf("EOF: %v", err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("oversize frame accepted")
	}
	oversized := &telemetryv1.TelemetryFrame{}
	// Unknown length-delimited field 2000 carries 4 MiB, pushing protobuf size over the cap.
	oversized.ProtoReflect().SetUnknown(append([]byte{0x82, 0x7d}, append([]byte{0x80, 0x80, 0x80, 0x02}, make([]byte, MaxFrameSize)...)...))
	if err := WriteFrame(io.Discard, oversized); err == nil {
		t.Fatal("oversize write accepted")
	}
	binary.BigEndian.PutUint32(header[:], 2)
	if _, err := ReadFrame(bytes.NewReader(append(header[:], 1))); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestNDJSONDecoder(t *testing.T) {
	d := NewNDJSONDecoder(strings.NewReader("{\"sequence\":\"1\"}\n{\"sequence\":\"2\"}\n"))
	for i := uint64(1); i <= 2; i++ {
		f, err := d.Next()
		if err != nil || f.Sequence != i {
			t.Fatalf("frame %v, %v", f, err)
		}
	}
	if _, err := d.Next(); err != io.EOF {
		t.Fatalf("EOF: %v", err)
	}
	if _, err := NewNDJSONDecoder(strings.NewReader(strings.Repeat("x", MaxFrameSize+1) + "\n")).Next(); err == nil {
		t.Fatal("oversize NDJSON accepted")
	}
	if _, err := NewNDJSONDecoder(strings.NewReader("garbage\n")).Next(); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}
