package protocol

import (
	"reflect"
	"testing"
)

// offset is the fixed byte offset used across the reader-helper tests; the fixtures place the
// value 42 there so each helper's result and return type can be asserted.
var offset = 2

// TestU16 checks u16 reads a little-endian uint16 at the given offset.
func TestU16(t *testing.T) {
	var data = []byte{0x00, 0x00, 0x2a, 0x00}
	result := u16(data, offset)
	if result != 42 {
		t.Errorf("%#x: want 42, got %d", data, result)
	}
	if typ := reflect.TypeOf(result); typ.String() != "uint16" {
		t.Errorf("want uint16, got %s", typ.String())
	}
}

// TestI16 checks i16 reads a little-endian signed int16 at the given offset.
func TestI16(t *testing.T) {
	var data = []byte{0x00, 0x00, 0x2a, 0x00}
	result := i16(data, offset)
	if result != 42 {
		t.Errorf("%#x: want 42, got %d", data, result)
	}
	if typ := reflect.TypeOf(result); typ.String() != "int16" {
		t.Errorf("want int16, got %s", typ.String())
	}
}

// TestU32 checks u32 reads a little-endian uint32 at the given offset.
func TestU32(t *testing.T) {
	var data = []byte{0x00, 0x00, 0x2a, 0x00, 0x00, 0x00}
	result := u32(data, offset)
	if result != 42 {
		t.Errorf("%#x: want 42, got %d", data, result)
	}
	if typ := reflect.TypeOf(result); typ.String() != "uint32" {
		t.Errorf("want uint32, got %s", typ.String())
	}
}

// TestI32 checks i32 reads a little-endian signed int32 at the given offset.
func TestI32(t *testing.T) {
	var data = []byte{0x00, 0x00, 0x2a, 0x00, 0x00, 0x00}
	result := i32(data, offset)
	if result != 42 {
		t.Errorf("%#x: want 42, got %d", data, result)
	}
	if typ := reflect.TypeOf(result); typ.String() != "int32" {
		t.Errorf("want int32, got %s", typ.String())
	}
}

// TestF32 checks f32 reads an IEEE-754 float32 at the given offset.
func TestF32(t *testing.T) {
	var data = []byte{0x00, 0x00, 0x00, 0x00, 0x28, 0x42}
	result := f32(data, offset)
	if result != 42.0 {
		t.Errorf("%#x: want 42.0, got %.1f", data, result)
	}
	if typ := reflect.TypeOf(result); typ.String() != "float32" {
		t.Errorf("want float32, got %s", typ.String())
	}
}

// TestBit checks bit extracts each individual bit position from a byte, high bit included.
func TestBit(t *testing.T) {
	var b byte = 0b10001101
	tests := []struct {
		name string
		n    uint
		want bool
	}{
		{"bit 0 set", 0, true}, {"bit 1 clear", 1, false}, {"bit 2 set", 2, true},
		{"bit 3 set", 3, true}, {"bit 4 clear", 4, false}, {"bit 5 clear", 5, false},
		{"bit 6 clear", 6, false}, {"bit 7 set", 7, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ok := bit(b, tt.n); ok != tt.want {
				t.Errorf("bit %d check failed: want %t, got %t", tt.n, tt.want, ok)
			}
		})
	}
}
