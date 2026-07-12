package protocol

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// TestDecryptFormatC is a regression test verifying Decrypt accepts GT7's format-C packets
// (368 bytes). It checks Decrypt decrypts them under the format-C IV mask (0xDEADBEEF), returns a
// 368-byte plaintext carrying the valid GT7 magic at offset 0, and pins packet_c_00's PackageID
// (92244) so a wrong mask can't slip through on a magic-byte coincidence alone.
func TestDecryptFormatC(t *testing.T) {
	const packetSizeC = 368
	const wantPackageIDPacketC00 = 92244

	for i := range 8 {
		name := fmt.Sprintf("packet_c_%02d.bin", i)
		t.Run(name, func(t *testing.T) {
			b := readFixture(t, name)
			dec, err := Decrypt(b)
			if err != nil {
				t.Fatalf("Decrypt(%s) returned error: %v", name, err)
			}
			if len(dec) != packetSizeC {
				t.Fatalf("Decrypt(%s): want %d byte plaintext, got %d", name, packetSizeC, len(dec))
			}
			magicCheck := binary.LittleEndian.Uint32(dec[:4])
			if magicCheck != magicNumber {
				t.Errorf("Decrypt(%s): want magic %#x, got %#x", name, magicNumber, magicCheck)
			}

			if name == "packet_c_00.bin" {
				gotPackageID := binary.LittleEndian.Uint32(dec[offPackageID : offPackageID+4])
				if gotPackageID != wantPackageIDPacketC00 {
					t.Errorf("Decrypt(%s): want PackageID %d, got %d (wrong IV mask would still "+
						"pass a magic check on a random collision, so pin the identity field too)",
						name, wantPackageIDPacketC00, gotPackageID)
				}
			}
		})
	}
}
