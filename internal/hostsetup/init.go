package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// DefaultTailscalePort is the HTTPS port tailscale serve publishes LCM on.
const DefaultTailscalePort = 9443

// DefaultConfig returns a configuration for operator with the default
// repository and port and the toolchains in the operator's ~/.local.
func DefaultConfig(ctx context.Context, h Host, operator string) (Config, error) {
	op, ok, err := lookupAccount(ctx, h.Runner, operator)
	if err != nil {
		return Config{}, err
	}
	if !ok {
		return Config{}, fmt.Errorf("no such account: %s", operator)
	}
	return Config{
		Operator:   operator,
		Repository: DefaultRepository,
		Toolchains: Toolchains{Go: filepath.Join(op.Home, ".local/go"), Node: filepath.Join(op.Home, ".local/node")},
		Tailscale:  Tailscale{Port: DefaultTailscalePort},
	}, nil
}

// Init writes a first configuration file, root:root 0644. It never
// replaces an existing file.
func Init(h Host, path string, c Config) error {
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host init")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("--config must be an absolute, clean path (got %q)", path)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	fsys := h.FS
	dir := filepath.Dir(path)
	if _, err := fsys.Lstat(dir); errors.Is(err, fs.ErrNotExist) && dir == EtcDir {
		if err := trusted(fsys, filepath.Dir(dir)); err != nil {
			return err
		}
		if err := fsys.Mkdir(dir, 0o755, 0, 0); err != nil {
			return err
		}
	}
	if err := trusted(fsys, dir); err != nil {
		return err
	}
	_, err := fsys.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%s already exists; edit it, or remove it to start over", path)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return fsys.WriteFile(path, bytes.NewReader(c.Encode()), 0o644, 0, 0)
}
