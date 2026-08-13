package work

import (
	"crypto/sha256"
	"encoding/hex"
)

// Identity is a stable opaque identifier derived from the canonical works root
// and exact Work name. It is used only as an ownership key, never as a path.
type Identity string

func NewIdentity(canonicalWorksRoot string, name Name) Identity {
	hash := sha256.New()
	_, _ = hash.Write([]byte(canonicalWorksRoot))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(name))
	return Identity(hex.EncodeToString(hash.Sum(nil)))
}

func (i Identity) String() string { return string(i) }
