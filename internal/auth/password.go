package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// MinPasswordLength is the minimum accepted password length in bytes.
const MinPasswordLength = 8

// Argon2id parameters (OWASP minimum: m=19 MiB, t=2, p=1). Kept small because
// LCM targets a resident footprint under 50 MB.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 19 * 1024 // KiB
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	saltLen             = 16

	// Upper bounds applied to stored hashes so that a tampered row cannot
	// make verification allocate unbounded memory.
	maxArgonMemory uint32 = 1 << 20 // KiB
	maxArgonTime   uint32 = 16
	maxArgonThread uint8  = 32
)

var b64 = base64.RawStdEncoding

// kdfSlot serializes argon2 evaluations so concurrent logins cannot multiply
// the per-hash memory cost.
var kdfSlot = make(chan struct{}, 1)

func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	kdfSlot <- struct{}{}
	defer func() { <-kdfSlot }()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// HashPassword returns a PHC-formatted argon2id hash:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash. A
// malformed hash is an error; a mismatch is (false, nil).
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "" "argon2id" "v=19" "m=..,t=..,p=.." salt hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	var (
		memory  uint32
		time    uint32
		threads uint8
	)
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, errors.New("auth: invalid argon2 parameters")
	}
	if memory == 0 || time == 0 || threads == 0 ||
		memory > maxArgonMemory || time > maxArgonTime || threads > maxArgonThread {
		return false, errors.New("auth: argon2 parameters out of range")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("auth: invalid password hash salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("auth: invalid password hash")
	}
	got := idKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // length of a decoded hash, bounded by the stored value
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
