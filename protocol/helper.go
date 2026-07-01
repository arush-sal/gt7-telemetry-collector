package protocol

import (
	"encoding/binary"
	"math"
)

// u16 converts a raw byte slice to uint16
func u16(b []byte, offset int) uint16 {
	offsetCheck16(offset, len(b))
	return binary.LittleEndian.Uint16(b[offset : offset+2])
}

// i16 converts a raw byte slice to int16
func i16(b []byte, offset int) int16 {
	return int16(u16(b, offset))
}

// u32 converts a raw byte slice to uint32
func u32(b []byte, offset int) uint32 {
	offsetCheck32(offset, len(b))
	return binary.LittleEndian.Uint32(b[offset : offset+4])
}

// i32 converts a raw byte slice to int32
func i32(b []byte, offset int) int32 {
	return int32(u32(b, offset))
}

// f32 converts a raw byte slice to float32
func f32(b []byte, offset int) float32 {
	offsetCheck32(offset, len(b))
	return math.Float32frombits(u32(b, offset))
}

// bit does bit things
func bit(b byte, n uint) bool { return b&(1<<n) != 0 }

func offsetCheck16(offset int, b int) {
	if offset < 0 || offset+2 > b {
		panic("Error: Buffer is too small or offset is out of bounds")
	}
}

func offsetCheck32(offset int, b int) {
	if offset < 0 || offset+4 > b {
		panic("Error: Buffer is too small or offset is out of bounds")
	}
}
