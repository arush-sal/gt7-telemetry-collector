package transport

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	telemetryv1 "github.com/arush-sal/gt7-telemetry-collector/gen/gt7/telemetry/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// MaxFrameSize bounds a protobuf frame or NDJSON line to 4 MiB, limiting consumer allocations.
const MaxFrameSize = 4 * 1024 * 1024

// WriteFrame writes a four-byte big-endian length followed by protobuf bytes.
func WriteFrame(w io.Writer, f *telemetryv1.TelemetryFrame) error {
	if proto.Size(f) > MaxFrameSize {
		return fmt.Errorf("transport: frame exceeds %d bytes", MaxFrameSize)
	}
	b, err := proto.Marshal(f)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// ReadFrame reads one length-prefixed protobuf frame, rejecting oversized lengths before allocation.
func ReadFrame(r io.Reader) (*telemetryv1.TelemetryFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("transport: frame exceeds %d bytes", MaxFrameSize)
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	f := new(telemetryv1.TelemetryFrame)
	if err := proto.Unmarshal(b, f); err != nil {
		return nil, err
	}
	return f, nil
}

// NDJSONDecoder reads protojson frames, one per line, bounded by MaxFrameSize.
type NDJSONDecoder struct{ scanner *bufio.Scanner }

// NewNDJSONDecoder creates a decoder with a MaxFrameSize line cap, excluding the newline.
func NewNDJSONDecoder(r io.Reader) *NDJSONDecoder {
	s := bufio.NewScanner(r)
	// Start with 4 KiB; one extra byte at the cap allows the newline after a maximum-size line.
	s.Buffer(make([]byte, 4096), MaxFrameSize+1)
	return &NDJSONDecoder{scanner: s}
}

// Next decodes the next frame, returning io.EOF at the end of the stream.
func (d *NDJSONDecoder) Next() (*telemetryv1.TelemetryFrame, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	b := d.scanner.Bytes()
	if len(b) > MaxFrameSize {
		return nil, fmt.Errorf("transport: line exceeds %d bytes", MaxFrameSize)
	}
	f := new(telemetryv1.TelemetryFrame)
	if err := protojson.Unmarshal(b, f); err != nil {
		return nil, err
	}
	return f, nil
}
