package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
)

func TestListBackups(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	if n := len(x.call("GET", v1+"/resources/web/backups", nil).want(http.StatusOK).items()); n != 0 {
		t.Fatalf("backups = %d", n)
	}
	rec := catalog.BackupRecord{
		Resource: "web", Kind: catalog.BackupKindBackup, BackupID: "20261101T120000Z-0a1b2c3d", Outcome: catalog.OutcomeSucceeded,
		Size: 4096, ManifestSHA256: strings.Repeat("a", 64), Skipped: 2, OperationID: "00000000-0000-4000-8000-000000000001",
		Sources: []model.BackupSource{{Type: "files", Path: "media"}}, Actor: auth.Actor{Kind: auth.KindSystem, ID: "lcm", Name: "lcm"},
	}
	if _, err := x.app.Catalog.AddBackup(x.ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Kind, rec.BackupID = catalog.BackupKindPreRestore, "pre-restore-20261101T130000Z-0a1b2c3d"
	if _, err := x.app.Catalog.AddBackup(x.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := x.app.Catalog.BeginBackup(x.ctx, catalog.BackupRecord{Resource: "web", Kind: catalog.BackupKindBackup, Actor: rec.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := x.app.Catalog.MarkBackupsPruned(x.ctx, "web", []string{"20261101T120000Z-0a1b2c3d"}); err != nil {
		t.Fatal(err)
	}

	items := x.call("GET", v1+"/resources/web/backups", nil).want(http.StatusOK).items()
	if len(items) != 3 || items[0]["outcome"] != "running" || items[0]["restorable"] != false || items[0]["finishedAt"] != nil {
		t.Fatalf("backups = %v", items)
	}
	pre, old := items[1], items[2]
	if pre["kind"] != "pre-restore" || pre["restorable"] != true || pre["size"] != float64(4096) || pre["skipped"] != float64(2) ||
		pre["sources"].([]any)[0].(map[string]any)["path"] != "media" {
		t.Fatalf("pre-restore = %v", pre)
	}
	if old["restorable"] != false || old["prunedAt"] == nil {
		t.Fatalf("removed set = %v", old)
	}

	page := x.call("GET", v1+"/resources/web/backups?limit=2", nil).want(http.StatusOK).obj()
	if page["nextCursor"] == nil || len(page["items"].([]any)) != 2 {
		t.Fatalf("page = %v", page)
	}
	if next := x.call("GET", v1+"/resources/web/backups?limit=2&cursor="+page["nextCursor"].(string), nil).items(); len(next) != 1 {
		t.Fatalf("next page = %v", next)
	}
	x.call("GET", v1+"/resources/web/backups?cursor=bm9wZQ", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/resources/nope/backups", nil).wantError(http.StatusNotFound, "not_found")
}

func TestRestoreActionRequest(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	// Only restore takes a backup ID, and it must look like one.
	x.call("POST", v1+"/resources/web/actions/stop", map[string]string{"backupId": "20261101T120000Z-0a1b2c3d"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/resources/web/actions/restore", map[string]string{"backupId": "latest"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	// A service without a typed backup has nothing to restore.
	x.call("POST", v1+"/resources/web/actions/restore", nil).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", v1+"/resource-groups/actions", map[string]any{
		"selector": map[string]any{"type": "service", "labels": map[string]string{}}, "action": "restore",
	}).wantError(http.StatusUnprocessableEntity, "validation_failed")
}
