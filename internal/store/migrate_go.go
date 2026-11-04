package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

// goMigrations holds data conversions that SQL cannot express, keyed by the
// version of the migration they belong to. Each runs after the SQL of its
// migration in the same transaction, so a failure leaves the database at the
// previous version.
var goMigrations = map[int]func(context.Context, *sql.Tx) error{
	14: migrateResources,
}

// NewID returns a random identifier in UUID version 4 form, the format of
// resource and operation IDs.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
