package main

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/ACaccel/lcm/internal/deployhelper"
)

// backupWorker is the unprivileged process that the deploy helper starts as
// a service's own account in a sandbox to produce or restore backup
// artifacts. It reads its specification from the input directory the helper
// bound and refuses to run as root.
func (a *app) backupWorker(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return usagef("backup-worker takes the input and the output directory")
	}
	// A process of the same user can read /proc/<pid>/root of a dumpable
	// process and so reach the staging output after the worker is gone. This
	// covers the worker itself; the programs it starts become dumpable again
	// when they are executed.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("make the worker undumpable: %w", err)
	}
	return deployhelper.RunWorker(ctx, deployhelper.DefaultWorkerHost(), args[0], args[1], a.stdout)
}
