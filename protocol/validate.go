package protocol

import "fmt"

// Validate reports whether dec is long enough for Decode to read every field
// without panicking; it errors if the slice is shorter than minDecodeLen.
func Validate(dec []byte) error {
	if len(dec) < minDecodeLen {
		return fmt.Errorf("malformed packet: expected %d bytes, got %d", minDecodeLen, len(dec))
	}
	return nil
}
