package work

import "fmt"

const MaxNameBytes = 128

// Name is both the Work directory name and the local branch name.
// It always contains the exact bytes supplied by the user.
type Name string

// ParseName validates the filesystem-safe, user-facing part of the Work name
// contract. Git-specific ref validation is performed by the Git adapter.
func ParseName(raw string) (Name, error) {
	if raw == "" {
		return "", fmt.Errorf("work name is empty")
	}
	if len(raw) > MaxNameBytes {
		return "", fmt.Errorf("work name is %d bytes; maximum is %d", len(raw), MaxNameBytes)
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		valid := c >= 'A' && c <= 'Z' ||
			c >= 'a' && c <= 'z' ||
			c >= '0' && c <= '9' ||
			(i > 0 && (c == '.' || c == '_' || c == '-'))
		if !valid {
			return "", fmt.Errorf("work name contains invalid byte %q at offset %d", c, i)
		}
	}
	return Name(raw), nil
}

func (n Name) String() string { return string(n) }
