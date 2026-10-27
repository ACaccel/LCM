package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	lcmapp "github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/config"
)

// closeTimeout bounds how long in-flight policy nodes may delay shutdown;
// their runs resume on the next start.
const closeTimeout = 10 * time.Second

func (a *app) serve(ctx context.Context, args []string) error {
	cfg, err := config.FromEnv(a.getenv)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(a.stderr, "Usage: lcm serve [flags]")
		fs.PrintDefaults()
	}
	cfg.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("serve: unexpected argument %q", fs.Arg(0))
	}
	instance, err := lcmapp.New(ctx, cfg, lcmapp.WithLogger(cfg.NewLogger(a.stderr)))
	if err != nil {
		return err
	}
	if a.onListen != nil {
		a.onListen(instance.Addr())
	}
	runErr := instance.Run(ctx)

	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	if err := instance.Close(closeCtx); err != nil && runErr == nil {
		return err
	}
	return runErr
}
