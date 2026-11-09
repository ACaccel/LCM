package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/hostsetup"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// deployHelper is the installed deploy helper, called through its sudo
// wrapper. It is ready only while the root-owned wrapper exists, checked
// at every use so that installing the deploy component needs no restart
// of LCM.
type deployHelper struct {
	client  deployhelper.Client
	wrapper string
	// workDir is LCM's build directory; the helper builds from the
	// checkout in /var/lib/lcm/work only.
	workDir string
}

func (h deployHelper) Call(ctx context.Context, req deployhelper.Request) (deployhelper.Response, error) {
	return h.client.Call(ctx, req)
}

func (h deployHelper) Ready() error {
	info, err := os.Lstat(h.wrapper)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w (%s is missing)", rt.ErrHelperUnavailable, h.wrapper)
	}
	if err != nil {
		return fmt.Errorf("%w (%w)", rt.ErrHelperUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w (%s is not a root-owned file that only root can write)", rt.ErrHelperUnavailable, h.wrapper)
	}
	if h.workDir != hostsetup.WorkDir {
		return fmt.Errorf("%w (the helper builds from %s, but this server's build directory is %s)",
			rt.ErrHelperUnavailable, hostsetup.WorkDir, h.workDir)
	}
	return nil
}
