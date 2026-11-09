package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/store"
)

// storageSchema prints the storage version this binary supports, or with
// --data-dir the one the database in that directory has reached. The second
// form only reads: it never creates or migrates a database, so "lcm host
// upgrade" can compare it with the supported version after a start.
func (a *app) storageSchema(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("storage-schema", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	dataDir := fs.String("data-dir", "", "print the schema of the database in this data directory instead of the supported one")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("storage-schema accepts no arguments")
	}
	var v int
	if *dataDir != "" {
		v, err = store.StoredSchemaVersion(ctx, filepath.Join(*dataDir, config.DBFileName))
	} else {
		v, err = store.SchemaVersion()
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.stdout, v)
	return err
}
