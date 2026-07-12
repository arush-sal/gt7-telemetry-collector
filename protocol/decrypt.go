package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/salsa20"
)

// ErrPacketLength is returned by Decrypt when the input's length does not match any known GT7
// packet format (A, B, "~", or C) — too short to decode, or a foreign/corrupt datagram. Callers can
// distinguish this from a bad-magic packet with errors.Is.
var ErrPacketLength = errors.New("protocol: packet is not a recognized GT7 packet length")

// ivMaskByLen maps a packet's on-wire length to the IV XOR mask for that GT7 format. The lengths
// and masks are reverse-engineered protocol facts, not choices (see constants.go for provenance and
// verification status of each). Every accepted length here is >= 0x44 bytes, so the 0x40 IV read in
// Decrypt below never runs out of bounds.
var ivMaskByLen = map[int]uint32{
	packetSizeA:     ivMaskA,
	packetSizeB:     ivMaskB,
	packetSizeTilde: ivMaskTilde,
	packetSizeC:     ivMaskC,
}

// ErrBadMagic is returned by Decrypt when the decrypted packet lacks the GT7 magic sentinel: a
// foreign or corrupt packet, or a wrong key/nonce.
var ErrBadMagic = errors.New("protocol: decrypted packet has wrong magic")

// salsaKey is the fixed 32-byte Salsa20 key shared by every GT7 packet (the first 32 bytes of a
// known protocol string). It is declared at package scope so it is built exactly once at program
// start rather than on every Decrypt call, which runs on the 60 Hz receive hot path. It is treated
// as immutable after initialization and never written again, so concurrent reads are safe.
var salsaKey = [32]byte([]byte("Simulator Interface Packet GT7 ver 0.0")[:32])

// Decrypt returns the plaintext of a single encrypted GT7 telemetry packet.
//
// GT7 encrypts each packet with Salsa20 under a fixed key, varying the nonce per packet. The nonce
// seed (an IV word) travels in the clear inside the packet at offset 0x40, so the receiver reads it
// to reconstruct the keystream. GT7 has four wire formats (A, B, "~", C), distinguished only by
// their length: each has its own fixed size and its own IV XOR mask (see ivMaskByLen). Decrypt looks
// up the mask by len(enc), derives the 8-byte nonce from the IV (low word = IV XOR that format's
// mask, high word = IV), runs the stream cipher, and verifies the magic sentinel to confirm the
// result is a genuine, correctly-decrypted GT7 packet.
//
// Every accepted format is a fixed size, so Decrypt requires len(enc) to match one of them and
// never panics: it returns ErrPacketLength for an unrecognized length and ErrBadMagic when the
// decrypted magic does not match. The input slice enc is not mutated: the cipher reads enc and
// writes a fresh output buffer.
func Decrypt(enc []byte) ([]byte, error) {
	// Look up this length's IV mask. An unrecognized length is rejected here — and this guard is
	// also what keeps the IV slice below from panicking, since every entry in ivMaskByLen is >= 0x44.
	mask, ok := ivMaskByLen[len(enc)]
	if !ok {
		return nil, fmt.Errorf("%w: got %d bytes", ErrPacketLength, len(enc))
	}

	// Read the per-packet IV word (clear text at 0x40) and derive the two nonce words from it.
	iv1 := binary.LittleEndian.Uint32(enc[0x40:0x44])
	iv2 := iv1 ^ mask

	// Nonce layout: little-endian iv2 in the low 4 bytes, little-endian iv1 in the high 4 bytes.
	var nonce = make([]byte, 8)
	binary.LittleEndian.PutUint32(nonce, iv2)
	binary.LittleEndian.PutUint32(nonce[4:], iv1)

	// Decrypt into a fresh buffer so enc stays untouched (callers reuse their receive buffer).
	var out = make([]byte, len(enc))
	salsa20.XORKeyStream(out, enc, nonce, &salsaKey)

	// The magic must reappear at offset 0, otherwise the key/nonce did not line up: not our packet.
	magicCheck := binary.LittleEndian.Uint32(out[:4])
	if magicCheck != magicNumber {
		return nil, fmt.Errorf("%w: want %#x, got %#x", ErrBadMagic, magicNumber, magicCheck)
	}

	return out, nil
}
