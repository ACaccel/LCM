package engine

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// crockford is the ULID alphabet.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idGen produces ULIDs: 48 bits of Unix milliseconds followed by 80 random
// bits, monotonic within one generator so that ids sort in creation order
// even when the clock does not advance (as with a fake clock).
type idGen struct {
	mu     sync.Mutex
	lastMs uint64
	hi     uint16 // top 16 bits of the random part
	lo     uint64 // low 64 bits of the random part
}

func (g *idGen) next(now time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := uint64(max(now.UnixMilli(), 0)) //nolint:gosec // clamped to non-negative
	if ms <= g.lastMs {
		ms = g.lastMs
		g.lo++
		if g.lo == 0 {
			g.hi++
		}
	} else {
		var b [10]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		g.lastMs = ms
		// Keep the top bit clear so increments cannot overflow the
		// 80-bit field within one millisecond.
		g.hi = binary.BigEndian.Uint16(b[:2]) & 0x7fff
		g.lo = binary.BigEndian.Uint64(b[2:])
	}
	return encodeULID(ms, g.hi, g.lo)
}

func encodeULID(ms uint64, hi uint16, lo uint64) string {
	var b [16]byte
	// The timestamp fills the first 6 bytes; bytes 6-7 start the random
	// part.
	binary.BigEndian.PutUint64(b[:8], ms<<16)
	binary.BigEndian.PutUint16(b[6:8], hi)
	binary.BigEndian.PutUint64(b[8:], lo)

	// 128 bits encoded as 26 base32 characters, most significant first;
	// the first character carries only 3 bits.
	var out [26]byte
	var acc uint64
	var accBits uint
	pos := 0
	// Prepend two zero bits so the total (130) is a multiple of 5.
	accBits = 2
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		accBits += 8
		for accBits >= 5 {
			accBits -= 5
			out[pos] = crockford[(acc>>accBits)&0x1f]
			pos++
		}
	}
	return string(out[:])
}
