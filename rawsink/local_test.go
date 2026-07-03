package rawsink

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestLocalSinkRoundTrip writes several records through the buffered sink and reads the file back,
// asserting the length-prefixed, timestamped framing round-trips exactly.
func TestLocalSinkRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "dump/raw.bin"
	s, err := NewLocalSink(fs, path, 16)
	if err != nil {
		t.Fatalf("NewLocalSink: %v", err)
	}

	want := []struct {
		ts      time.Time
		payload []byte
	}{
		{time.Unix(0, 1000), []byte{0x01, 0x02, 0x03}},
		{time.Unix(0, 2000), []byte("hello")},
		{time.Unix(0, 3000), []byte{0xff}},
	}
	for _, w := range want {
		if err := s.Write(w.ts, w.payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0", s.Dropped())
	}

	b, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	off := 0
	for i, w := range want {
		if off+recordHeaderSize > len(b) {
			t.Fatalf("record %d: truncated header", i)
		}
		n := binary.LittleEndian.Uint32(b[off : off+4])
		ts := int64(binary.LittleEndian.Uint64(b[off+4 : off+12]))
		off += recordHeaderSize
		if int(n) != len(w.payload) {
			t.Errorf("record %d: length = %d, want %d", i, n, len(w.payload))
		}
		if ts != w.ts.UnixNano() {
			t.Errorf("record %d: ts = %d, want %d", i, ts, w.ts.UnixNano())
		}
		if got := b[off : off+int(n)]; string(got) != string(w.payload) {
			t.Errorf("record %d: payload = %x, want %x", i, got, w.payload)
		}
		off += int(n)
	}
	if off != len(b) {
		t.Errorf("trailing bytes: consumed %d of %d", off, len(b))
	}
}

// TestLocalSinkWriteAfterClose confirms Write errors once the sink is closed and that Close is
// idempotent.
func TestLocalSinkWriteAfterClose(t *testing.T) {
	fs := afero.NewMemMapFs()
	s, err := NewLocalSink(fs, "raw.bin", 4)
	if err != nil {
		t.Fatalf("NewLocalSink: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close should be nil, got %v", err)
	}
	if err := s.Write(time.Unix(0, 1), []byte{0x01}); err == nil {
		t.Error("Write after Close should error")
	}
}
