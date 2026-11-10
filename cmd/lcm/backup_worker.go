package main

import (
	"context"

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
	return deployhelper.RunWorker(ctx, deployhelper.DefaultWorkerHost(), args[0], args[1], a.stdout)
}
