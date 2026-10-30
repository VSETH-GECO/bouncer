package mac

import (
	"testing"
)

func TestParse_AcceptedForms(t *testing.T) {
	tests := []struct {
		input    string
		expected string // hex form
	}{
		{"00:11:22:33:44:55", "001122334455"},
		{"00-11-22-33-44-55", "001122334455"},
		{"001122334455", "001122334455"},
		{"00:11:22:33:44:AB", "0011223344ab"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			mac, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v; want nil", tt.input, err)
			}
			if got := mac.Hex(); got != tt.expected {
				t.Errorf("Parse(%q).Hex() = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParse_Rejected(t *testing.T) {
	tests := []struct {
		input string
	}{
		{""},                  // empty
		{"00:11:22:33:44"},    // too few
		{"0011223344556677"},  // too many
		{"zz:11:22:33:44:55"}, // non-hex
		{"00:11-22:33:44:55"}, // mixed separators
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err == nil {
				t.Fatalf("Parse(%q) error = nil; want error", tt.input)
			}
		})
	}
}

func TestFromBytes(t *testing.T) {
	// Valid: 6 bytes
	mac, ok := FromBytes([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55})
	if !ok {
		t.Errorf("FromBytes with 6 bytes: ok = false; want true")
	}
	if mac.Hex() != "001122334455" {
		t.Errorf("FromBytes(6 bytes).Hex() = %q; want %q", mac.Hex(), "001122334455")
	}

	// Invalid: 5 bytes
	_, ok = FromBytes([]byte{0x00, 0x11, 0x22, 0x33, 0x44})
	if ok {
		t.Errorf("FromBytes with 5 bytes: ok = true; want false")
	}

	// Invalid: 7 bytes
	_, ok = FromBytes([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
	if ok {
		t.Errorf("FromBytes with 7 bytes: ok = true; want false")
	}

	// Invalid: nil
	_, ok = FromBytes(nil)
	if ok {
		t.Errorf("FromBytes with nil: ok = true; want false")
	}
}

func TestRenderings(t *testing.T) {
	mac := MustParse("00:11:22:33:44:AB")

	// String() should be colon-separated, lowercase
	if got := mac.String(); got != "00:11:22:33:44:ab" {
		t.Errorf("String() = %q; want %q", got, "00:11:22:33:44:ab")
	}

	// Bytes() should return raw bytes
	expected := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0xab}
	if got := mac.Bytes(); !bytesEqual(got, expected) {
		t.Errorf("Bytes() = %v; want %v", got, expected)
	}

	// IsZero() should be false
	if mac.IsZero() {
		t.Errorf("IsZero() = true; want false")
	}

	// Zero MAC should report IsZero() = true
	zeroMac := MAC{}
	if !zeroMac.IsZero() {
		t.Errorf("MAC{}.IsZero() = false; want true")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
