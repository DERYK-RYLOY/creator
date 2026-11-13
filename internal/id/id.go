// Package id produces short, lexicographically sortable identifiers used for
// jobs, handles, sessions and checkpoints.
package id

import (
	"crypto/rand"
	"encoding/base32"
	"time"
)

var enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New returns a sortable identifier. The first 8 bytes are the current
// millisecond timestamp (big endian) followed by 10 random bytes, so sorting
// identifiers lexicographically also sorts them by creation time while
// collisions remain negligible.
func New(prefix string) string {
	var b [18]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 7; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[8:]); err != nil {
		panic("creator: crypto/rand unavailable: " + err.Error())
	}
	s := enc.EncodeToString(b[:])
	if prefix == "" {
		return s
	}
	return prefix + "_" + s
}
