// Package model defines the Northplane domain model (SPEC §6).
//
// All persistent entities carry UUIDv7 identifiers (time-sortable,
// replication-friendly — ADR-09), RFC 3339 UTC timestamps and, where
// configurable, a monotonically increasing version for optimistic locking.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// uuidv7State guards monotonicity within a single millisecond.
var uuidv7State struct {
	sync.Mutex
	lastMillis int64
	seq        uint16
}

// NewID returns a UUIDv7 in canonical lowercase form.
//
// Layout per RFC 9562: 48-bit Unix milliseconds, 4-bit version (7),
// 12-bit monotonic sequence, 2-bit variant, 62 bits of randomness.
func NewID() string {
	return NewIDAt(time.Now())
}

// NewIDAt returns a UUIDv7 whose timestamp component is taken from t.
// Used by importers and tests that need deterministic time ordering.
func NewIDAt(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[8:]); err != nil {
		panic(fmt.Sprintf("model: crypto/rand failed: %v", err)) // no sane fallback
	}

	ms := t.UnixMilli()
	uuidv7State.Lock()
	if ms == uuidv7State.lastMillis {
		uuidv7State.seq++
	} else {
		uuidv7State.lastMillis = ms
		uuidv7State.seq = 0
	}
	seq := uuidv7State.seq & 0x0fff
	uuidv7State.Unlock()

	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | byte(seq>>8) // version 7
	b[7] = byte(seq)
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var dst [36]byte
	hex.Encode(dst[:8], b[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst[:])
}

var idRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidID reports whether s looks like a canonical UUID.
func ValidID(s string) bool { return idRe.MatchString(s) }

// IDTime extracts the embedded millisecond timestamp of a UUIDv7.
// Returns the zero time for malformed or non-v7 IDs.
func IDTime(id string) time.Time {
	if !ValidID(id) || id[14] != '7' {
		return time.Time{}
	}
	raw, err := hex.DecodeString(id[:8] + id[9:13])
	if err != nil {
		return time.Time{}
	}
	ms := int64(raw[0])<<40 | int64(raw[1])<<32 | int64(raw[2])<<24 |
		int64(raw[3])<<16 | int64(raw[4])<<8 | int64(raw[5])
	return time.UnixMilli(ms).UTC()
}

// NewSecret returns a URL-safe random token body of n bytes entropy,
// hex-encoded. Callers add purpose prefixes ("np_", "ap_", …).
func NewSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("model: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
