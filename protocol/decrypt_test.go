package protocol

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// TestDecrypt checks every fixture decrypts to a plaintext carrying the GT7 magic at offset 0.
func TestDecrypt(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "packet_*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		b := readFixture(t, filepath.Base(m))
		dec, err := Decrypt(b)
		if err != nil {
			t.Fatalf("unable to decrypt %s fixture, got: %v", m, err)
		}
		magicCheck := binary.LittleEndian.Uint32(dec[:4])
		if magicCheck != magicNumber {
			t.Errorf("%s: want %#x magic number, got %#x", m, magicNumber, magicCheck)
		}
	}
}

// TestDecryptErrors checks Decrypt returns an error (never panics) on a packet too short to hold
// the IV and on a packet whose IV was corrupted (wrong nonce -> magic mismatch).
func TestDecryptErrors(t *testing.T) {
	// build any inputs that need construction BEFORE the table:
	goodIV := readFixture(t, "packet_00.bin")
	badIV := make([]byte, len(goodIV))
	copy(badIV, goodIV)
	badIV[0x40] ^= 0xFF

	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{"too short", badIV[:11], true},
		{"bad iv", badIV, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decrypt(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("Decrypt() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
