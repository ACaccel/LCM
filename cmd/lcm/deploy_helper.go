package main

import (
	"context"

	"github.com/ACaccel/lcm/internal/deployhelper"
)

// deployHelper accepts only the fixed root-owned wrapper protocol on stdin;
// refusals are replies, not errors.
func (a *app) deployHelper(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return usagef("deploy-helper accepts no arguments")
	}
	return deployhelper.ServeLocal(ctx, a.stdin, a.stdout)
}
