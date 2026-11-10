package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"syscall"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/upgrademarker"
)

// selfResource is the service LCM deploys itself through (deploy/seed.sh
// reserves the name).
const selfResource = "lcm"

// reconcileHostUpgrade consumes the marker that "lcm host upgrade" leaves in
// the data directory. The upgrade replaces the binary outside the catalog, so
// the newest successful deployment of selfResource still names the previous
// commit and its version check stays down. When the marker names the commit
// this process runs, a deployment attributed to the upgrade is recorded; a
// marker for any other build is left alone, since it says nothing about this
// process. The marker is removed once its content is reflected in the
// catalog, and kept when recording failed so that the next start retries.
func reconcileHostUpgrade(ctx context.Context, log *slog.Logger, cat *catalog.Catalog, dataDir, running string) {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		log.Warn("host upgrade marker: open data directory", "error", err)
		return
	}
	defer func() { _ = root.Close() }()
	m, err := readHostUpgradeMarker(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		log.Warn("host upgrade marker ignored", "file", upgrademarker.File, "error", err)
		return
	}
	res, err := cat.RecordHostUpgrade(ctx, selfResource, m, running)
	if err != nil {
		log.Error("record host upgrade failed", "resource", selfResource, "error", err)
		return
	}
	switch res {
	case catalog.HostUpgradeForeign:
		log.Info("host upgrade marker names another build; left in place", "marker", m.To.Commit, "running", running)
		return
	case catalog.HostUpgradeRecorded:
		log.Info("recorded the host upgrade", "resource", selfResource, "from", m.From.Commit, "to", m.To.Commit)
	}
	if err := root.Remove(upgrademarker.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("remove host upgrade marker failed", "file", upgrademarker.File, "error", err)
	}
}

// readHostUpgradeMarker reads the marker without following a link, from a
// regular file of bounded size.
func readHostUpgradeMarker(root *os.Root) (upgrademarker.Marker, error) {
	f, err := root.OpenFile(upgrademarker.File, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return upgrademarker.Marker{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return upgrademarker.Marker{}, err
	}
	if !info.Mode().IsRegular() {
		return upgrademarker.Marker{}, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, upgrademarker.MaxSize+1))
	if err != nil {
		return upgrademarker.Marker{}, err
	}
	m, err := upgrademarker.Decode(data)
	if err != nil {
		return upgrademarker.Marker{}, fmt.Errorf("invalid: %w", err)
	}
	return m, nil
}
