package main

import (
	"context"
	"errors"

	"github.com/ACaccel/lcm/internal/machine/power"
)

// machineHelper accepts only the fixed root-owned wrapper protocol on stdin.
func (a *app) machineHelper(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return usagef("machine-helper accepts no arguments")
	}
	err := power.ServeLocal(ctx, a.stdin, a.stdout)
	var rejection *power.ProtocolError
	if errors.As(err, &rejection) {
		return nil
	}
	return err
}
