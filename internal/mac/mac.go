package mac

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// MAC represents a 6-byte MAC address.
type MAC [6]byte

// Parse parses a MAC address string in one of three forms:
// - Six groups of two hex digits separated by ':' (e.g., "00:11:22:33:44:55")
// - Six groups of two hex digits separated by '-' (e.g., "00-11-22-33-44-55")
// - Twelve bare hex digits (e.g., "001122334455")
//
// Letters in hex digits can be any case.
// Returns a descriptive error if the input is invalid.
func Parse(s string) (MAC, error) {
	if s == "" {
		return MAC{}, fmt.Errorf("parse MAC: empty input")
	}

	var hexStr string

	// Check for colon-separated form
	if strings.Count(s, ":") == 5 {
		parts := strings.Split(s, ":")
		if len(parts) != 6 {
			return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
		}
		for _, part := range parts {
			if len(part) != 2 {
				return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
			}
		}
		hexStr = strings.ToLower(strings.Join(parts, ""))
	} else if strings.Count(s, "-") == 5 {
		// Check for dash-separated form
		parts := strings.Split(s, "-")
		if len(parts) != 6 {
			return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
		}
		for _, part := range parts {
			if len(part) != 2 {
				return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
			}
		}
		hexStr = strings.ToLower(strings.Join(parts, ""))
	} else if strings.Count(s, ":") == 0 && strings.Count(s, "-") == 0 {
		// Bare hex digits
		if len(s) != 12 {
			return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
		}
		hexStr = strings.ToLower(s)
	} else {
		// Mixed separators or other invalid format
		return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
	}

	// Decode the hex string
	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return MAC{}, fmt.Errorf("parse MAC: invalid hex in %q", s)
	}

	if len(bytes) != 6 {
		return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
	}

	var result MAC
	copy(result[:], bytes)
	return result, nil
}

// MustParse parses a MAC address string and panics if it fails.
// This is intended for use in tests only.
func MustParse(s string) MAC {
	mac, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return mac
}

// FromBytes creates a MAC address from a byte slice.
// Returns false if the slice is not exactly 6 bytes.
func FromBytes(b []byte) (MAC, bool) {
	if b == nil || len(b) != 6 {
		return MAC{}, false
	}
	var result MAC
	copy(result[:], b)
	return result, true
}

// Hex returns the MAC address as twelve lowercase hex digits.
func (m MAC) Hex() string {
	return fmt.Sprintf("%012x", m[:])
}

// Bytes returns the MAC address as a raw 6-byte slice.
func (m MAC) Bytes() []byte {
	return m[:]
}

// String returns the MAC address in colon-separated form with lowercase hex digits.
func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// IsZero reports whether the MAC address is all zero bytes.
func (m MAC) IsZero() bool {
	for _, b := range m {
		if b != 0 {
			return false
		}
	}
	return true
}
