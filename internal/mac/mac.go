package mac

import (
	"encoding/hex"
	"fmt"
)

// MAC represents a 6-byte MAC address.
type MAC [6]byte

// Parse parses a MAC address string in one of three forms:
//   - 17 characters with ':' at positions 2, 5, 8, 11 and 14 (e.g., "00:11:22:33:44:55")
//   - 17 characters with '-' at those same positions (e.g., "00-11-22-33-44-55")
//   - 12 characters of bare hex digits (e.g., "001122334455")
//
// Hex digits can be in either case. Any other length, a separator off the
// grid, or mixed separators is rejected. The error wraps the hex decoding
// error when a character is not a hex digit.
func Parse(s string) (MAC, error) {
	if s == "" {
		return MAC{}, fmt.Errorf("parse MAC: empty input")
	}

	var hexStr string
	switch len(s) {
	case 12:
		hexStr = s
	case 17:
		sep := s[2]
		if sep != ':' && sep != '-' {
			return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
		}
		digits := make([]byte, 0, 12)
		for i := 0; i < len(s); i++ {
			if i%3 == 2 {
				if s[i] != sep {
					return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
				}
				continue
			}
			digits = append(digits, s[i])
		}
		hexStr = string(digits)
	default:
		return MAC{}, fmt.Errorf("parse MAC: invalid format %q", s)
	}

	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return MAC{}, fmt.Errorf("parse MAC: invalid hex in %q: %w", s, err)
	}

	var result MAC
	copy(result[:], decoded)
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
	if len(b) != 6 {
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
