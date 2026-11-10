package app_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
)

const managedWebBackup = managedWeb + `    backup:
      sources:
        - { type: files, path: media/ }
        - { type: sqlite, path: app.sqlite }
      keep: { daily: 3 }
`

// apiRequest sends a request as an administrator and returns the status and
// the decoded JSON object.
func apiRequest(t *testing.T, h *harness, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	_, token, err := h.app.Auth.CreateToken(h.ctx, "backup-"+method+path, auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

// TestTypedBackupAndRestoreThroughTheApp: the backup and restore of a
// managed service run through the deploy helper, are recorded, listed and
// audited, and a restore started over the API is undone-able by name.
func TestTypedBackupAndRestoreThroughTheApp(t *testing.T) {
	const v1 = "1111111111111111111111111111111111111111"
	helper := newFakeDeployHelper()
	runner := &execx.Fake{}
	runner.On(func(c execx.Cmd) bool { return slices.Contains(c.Argv, "rev-parse") }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{Output: v1 + "\n"}, nil
	})
	h := newHarness(t, func(o *testOpts) { o.deployHelper, o.runner = helper, runner })
	h.putService(managedWebBackup)
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()
	if _, err := h.app.Manager.Deploy(h.ctx, "web", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}

	reasons := actionReasons(apiCall(t, h, srv, "GET", "/api/v1/resources/web"))
	if reasons["backup"] != "" || reasons["restore"] != "" {
		t.Fatalf("reasons = %v", reasons)
	}
	res, err := h.app.Manager.Backup(h.ctx, "web", catalog.ActionOptions{})
	if err != nil || res.BackupID == "" {
		t.Fatalf("backup = %+v, %v", res, err)
	}

	code, list := apiRequest(t, h, srv, "GET", "/api/v1/resources/web/backups", nil)
	items := list["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("backups = %d %v", code, list)
	}
	row := items[0].(map[string]any)
	if row["backupId"] != res.BackupID || row["kind"] != "backup" || row["outcome"] != "succeeded" || row["restorable"] != true {
		t.Fatalf("row = %v", row)
	}

	// The dry run of a restore names the steps and calls nothing.
	before := len(helper.Requests())
	code, plan := apiRequest(t, h, srv, "POST", "/api/v1/resources/web/actions/restore?dryRun=true", map[string]string{"backupId": res.BackupID})
	if code != http.StatusOK {
		t.Fatalf("plan = %d %v", code, plan)
	}
	var codes []string
	for _, m := range plan["stepMessages"].([]any) {
		codes = append(codes, m.(map[string]any)["code"].(string))
	}
	if !slices.Contains(codes, "step.restorePreRestore") || !slices.Contains(codes, "step.restoreCheck") || len(helper.Requests()) != before {
		t.Fatalf("plan steps %v, helper calls %d", codes, len(helper.Requests())-before)
	}

	// A restore over the API runs in the background.
	code, accepted := apiRequest(t, h, srv, "POST", "/api/v1/resources/web/actions/restore", map[string]string{"backupId": res.BackupID})
	if code != http.StatusAccepted {
		t.Fatalf("restore = %d %v", code, accepted)
	}
	var action map[string]any
	for deadline := time.Now().Add(waitTimeout); ; {
		_, action = apiRequest(t, h, srv, "GET", "/api/v1/actions/"+accepted["actionId"].(string), nil)
		if action["status"] != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restore did not finish: %v", action)
		}
		time.Sleep(5 * time.Millisecond)
	}
	result, _ := action["result"].(map[string]any)
	if action["status"] != "succeeded" || result["backupId"] != res.BackupID || !strings.HasPrefix(result["preRestoreId"].(string), "pre-restore-") || action["backupId"] != res.BackupID {
		t.Fatalf("action = %v", action)
	}
	_, list = apiRequest(t, h, srv, "GET", "/api/v1/resources/web/backups", nil)
	kinds := map[string]int{}
	for _, it := range list["items"].([]any) {
		kinds[it.(map[string]any)["kind"].(string)]++
	}
	if kinds["backup"] != 1 || kinds["pre-restore"] != 1 || kinds["restore"] != 1 {
		t.Fatalf("rows by kind: %v", kinds)
	}
	var restores []deployhelper.Request
	for _, r := range helper.Requests() {
		if r.Op == deployhelper.OpRestore {
			restores = append(restores, r)
		}
	}
	if len(restores) != 1 || restores[0].BackupID != res.BackupID || restores[0].ManifestSHA256 == "" {
		t.Fatalf("restore requests = %+v", restores)
	}

	// Undo by name: the pre-restore set restores like any other.
	if _, err := h.app.Manager.Restore(h.ctx, "web", catalog.ActionOptions{BackupID: result["preRestoreId"].(string)}); err != nil {
		t.Fatal(err)
	}
	var audited []string
	for _, e := range h.auditEntries() {
		if strings.HasPrefix(e.Action, "service.backup") || strings.HasPrefix(e.Action, "service.restore") {
			audited = append(audited, e.Action)
		}
	}
	if !slices.Equal(audited, []string{"service.backup", "service.restore", "service.restore"}) && !slices.Equal(audited, []string{"service.restore", "service.restore", "service.backup"}) {
		t.Fatalf("audit = %v", audited)
	}
	// Groups never restore.
	if code, _ := apiRequest(t, h, srv, "POST", "/api/v1/resource-groups/actions", map[string]any{
		"selector": map[string]any{"type": "service", "labels": map[string]string{}}, "action": "restore",
	}); code != http.StatusUnprocessableEntity {
		t.Fatalf("group restore = %d", code)
	}
}

// TestSelfBackup: LCM backs up its own database and secrets into a private
// directory of its data directory, without the deploy helper, and records
// the backup under the resource lcm.
func TestSelfBackup(t *testing.T) {
	h := newHarness(t)
	h.putService("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: lcm }\nspec:\n  type: service\n  adapter: external\n  config: {}\n")
	secrets := filepath.Join(h.dir, "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "konata-token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h.app.SelfBackup == nil {
		t.Fatal("self backup is not wired")
	}
	res, err := h.app.SelfBackup.Run(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := filepath.Join(h.dir, "backups", res.BackupID)
	for p, mode := range map[string]os.FileMode{filepath.Join(h.dir, "backups"): 0o700, set + "/lcm.db": 0o600, set + "/secrets/konata-token": 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()
	_, list := apiRequest(t, h, srv, "GET", "/api/v1/resources/lcm/backups", nil)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "self" || items[0].(map[string]any)["backupId"] != res.BackupID || items[0].(map[string]any)["restorable"] != false {
		t.Fatalf("backups = %v", list)
	}
	found := false
	for _, e := range h.auditEntries() {
		found = found || e.Action == "lcm.backup" && e.Target == "lcm"
	}
	if !found {
		t.Error("the backup was not audited")
	}
}
