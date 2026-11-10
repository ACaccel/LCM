package catalog_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
)

// A set on the host that LCM has no record of, such as one copied from
// another machine, is adopted when a restore names it, or when no record
// exists and the newest is wanted.
func TestRestoreAdoptsASetTheHelperLists(t *testing.T) {
	e, h := backupEnv(t)
	const foreign = "20260101T000000Z-0a1b2c3d"
	h.mu.Lock()
	h.sets = append(h.sets,
		deployhelper.BackupInfo{BackupID: "pre-restore-20260102T000000Z-0a1b2c3d", Kind: deployhelper.KindPreRestore, CreatedAt: 3, ManifestSHA256: strings.Repeat("d", 64)},
		deployhelper.BackupInfo{BackupID: foreign, Kind: deployhelper.KindBackup, CreatedAt: 2, Size: 77, ManifestSHA256: strings.Repeat("e", 64),
			Sources: []deployhelper.BackupSource{{Type: "files", Path: "media"}}, Skipped: 3},
		deployhelper.BackupInfo{BackupID: "20250101T000000Z-0a1b2c3d", Kind: deployhelper.KindBackup, Problem: "the manifest is not valid"},
	)
	h.mu.Unlock()

	// The dry run finds it and records nothing.
	p, err := e.mgr.Plan(e.ctx, "app", catalog.ActionRestore, catalog.ActionOptions{})
	if err != nil || p.StepMessages[1].Params["backup"] != foreign {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if rows := backupRows(t, e); len(rows) != 0 {
		t.Fatalf("a dry run recorded %+v", rows)
	}

	res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.BackupID != foreign {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	row, err := e.cat.Backup(context.Background(), "app", foreign)
	if err != nil || row.Size != 77 || row.Skipped != 3 || row.ManifestSHA256 != strings.Repeat("e", 64) || row.Actor.Name != alice.Name || !strings.Contains(row.Error, "no record") {
		t.Fatalf("adopted row = %+v, %v", row, err)
	}
	var restore deployhelper.Request
	for _, r := range h.Requests() {
		if r.Op == deployhelper.OpRestore {
			restore = r
		}
	}
	if restore.BackupID != foreign || restore.ManifestSHA256 != strings.Repeat("e", 64) {
		t.Errorf("restore request = %+v", restore)
	}

	// An ID the helper does not list stays unknown, and so does a set whose
	// manifest cannot be read.
	for _, id := range []string{"20240101T000000Z-0a1b2c3d", "20250101T000000Z-0a1b2c3d"} {
		if _, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: id}); !errors.Is(err, catalog.ErrUnknownBackup) {
			t.Errorf("restore %s = %v", id, err)
		}
	}
}
