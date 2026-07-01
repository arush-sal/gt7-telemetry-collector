package protocol

import "testing"

// TestValidate checks a full-length decrypted packet passes and that truncated or empty slices
// return an error (so Decode never indexes out of bounds).
func TestValidate(t *testing.T) {
	dec, _ := Decrypt(readFixture(t, "packet_00.bin"))

	tests := []struct {
		name    string
		in      []byte
		wantErr bool
	}{
		{"full-length decrypted slice", dec, false},
		{"truncated slice", dec[:0x100], true},
		{"empty slice", make([]byte, 0), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
