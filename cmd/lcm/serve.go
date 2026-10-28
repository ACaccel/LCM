package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"runtime/debug"
	"time"

	lcmapp "github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/config"
)

// closeTimeout bounds how long in-flight policy nodes may delay shutdown;
// their runs resume on the next start.
const closeTimeout = 10 * time.Second

// applyMemoryLimit sets the soft memory limit of the Go runtime. A GOMEMLIMIT
// in the environment is the operator's choice and wins over the built-in
// default, but not over an explicit LCM setting.
func applyMemoryLimit(cfg config.Config, explicit, goMemLimitSet bool) error {
	limit, err := cfg.MemoryLimitBytes()
	if err != nil {
		return err
	}
	if goMemLimitSet && !explicit {
		return nil
	}
	if limit > 0 {
		debug.SetMemoryLimit(limit)
	} else if explicit {
		debug.SetMemoryLimit(math.MaxInt64)
	}
	return nil
}

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
	explicit := a.getenv(config.EnvMemoryLimit) != ""
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "memory-limit" })
	if err := applyMemoryLimit(cfg, explicit, a.getenv("GOMEMLIMIT") != ""); err != nil {
		return err
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
