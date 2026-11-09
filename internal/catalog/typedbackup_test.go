package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

const typedBackupConfig = `    backup:
      sources:
        - { type: files, path: media/ }
        - { type: sqlite, path: app.sqlite }
        - { type: command, argv: [bin/dump] }
      mode: stopped
      keep: { daily: 2, weekly: 1 }
`

// backupEnv is a deployed managed service with a typed backup.
func backupEnv(t *testing.T) (*env, *simHelper) {
	t.Helper()
	g := newGitFixture(t)
	h := newSimHelper()
	e := newEnv(t, realGit(g), withHelper(h))
	e.put(managedDocWith(g.remote, typedBackupConfig), 0)
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	h.pruneKeeps = 100
	return e, h
}

func backupRows(t *testing.T, e *env) []catalog.BackupRecord {
	t.Helper()
	rows, err := e.cat.Backups(context.Background(), "app", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func opsAfter(h *simHelper, n int) []deployhelper.Op {
	var out []deployhelper.Op
	for _, r := range h.Requests()[n:] {
		out = append(out, r.Op)
	}
	return out
}

func TestTypedBackup(t *testing.T) {
	e, h := backupEnv(t)
	n := len(h.Requests())

	// The operation ID is stored before the call.
	h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
		if req.Op == deployhelper.OpBackup {
			rows := backupRows(t, e)
			if len(rows) != 1 || rows[0].Outcome != catalog.OutcomeRunning || rows[0].OperationID != req.OperationID || rows[0].Kind != catalog.BackupKindBackup {
				t.Errorf("backup sent before its record: %+v", rows)
			}
		}
		return deployhelper.Response{}, nil, false
	}
	res, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	h.intercept = nil
	if err != nil {
		t.Fatal(err)
	}
	if got := opsAfter(h, n); !slices.Equal(got, []deployhelper.Op{deployhelper.OpBackup, deployhelper.OpPrune}) {
		t.Fatalf("ops = %v", got)
	}
	req := h.Requests()[n]
	if req.Service != "app" || req.Mode != "stopped" || req.TimeoutSec != 1800 || req.Runtime != "node24" || len(req.Sources) != 3 ||
		req.Sources[0].Type != "files" || req.Sources[0].Path != "media" || !slices.Equal(req.Sources[2].Argv, []string{"bin/dump"}) {
		t.Fatalf("backup request = %+v", req)
	}
	if pr := h.Requests()[n+1]; pr.Keep == nil || *pr.Keep != (deployhelper.Keep{Daily: 2, Weekly: 1}) {
		t.Fatalf("prune request = %+v", pr)
	}
	rows := backupRows(t, e)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	r := rows[0]
	if r.Outcome != catalog.OutcomeSucceeded || r.BackupID != res.BackupID || r.BackupID == "" || r.Size != 4096 || r.Skipped != 1 || r.ManifestSHA256 == "" ||
		r.Actor.Name != alice.Name || r.FinishedAt.IsZero() || len(r.Sources) != 3 || !r.Restorable() {
		t.Fatalf("row = %+v", r)
	}
	if !strings.Contains(res.Output, "backup "+res.BackupID) || !strings.Contains(res.Output, "1 entries") {
		t.Errorf("output %q", res.Output)
	}
	if e.state("app") != catalog.StateRunning {
		t.Errorf("state %s", e.state("app"))
	}

	// The timeline and the audit trail carry the set.
	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: "app", Kinds: []string{catalog.KindBackup}})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Data["backupId"] != res.BackupID {
		t.Fatalf("timeline = %+v, %v", page, err)
	}

	// Sets that retention removed are marked and no longer restorable.
	h.mu.Lock()
	h.pruneKeeps = 1
	h.mu.Unlock()
	second, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || !strings.Contains(second.Output, "1 old sets removed") {
		t.Fatalf("second = %+v, %v", second, err)
	}
	old, err := e.cat.Backup(context.Background(), "app", res.BackupID)
	if err != nil || old.PrunedAt.IsZero() || old.Restorable() {
		t.Fatalf("old = %+v, %v", old, err)
	}
	if latest, err := e.cat.LatestBackup(context.Background(), "app"); err != nil || latest.BackupID != second.BackupID {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}

func TestTypedBackupFailures(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		e, h := backupEnv(t)
		h.refuse[deployhelper.OpBackup] = deployhelper.CodeFailed
		_, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
		var he *rt.HelperError
		if !errors.As(err, &he) || he.Code != deployhelper.CodeFailed {
			t.Fatalf("err = %v", err)
		}
		rows := backupRows(t, e)
		if len(rows) != 1 || rows[0].Outcome != catalog.OutcomeFailed || !strings.Contains(rows[0].Error, "refused by the simulation") || rows[0].BackupID != "" {
			t.Fatalf("rows = %+v", rows)
		}
		// The service was not stopped by LCM, so its state is untouched.
		if e.state("app") != catalog.StateRunning {
			t.Errorf("state %s", e.state("app"))
		}
	})
	t.Run("a lost reply is looked up", func(t *testing.T) {
		e, h := backupEnv(t)
		h.lose[deployhelper.OpBackup] = true
		res, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
		// The ledger says it succeeded, so the backup did.
		if err != nil || res.BackupID == "" {
			t.Fatalf("res = %+v, %v", res, err)
		}
	})
	t.Run("an unknown outcome is never assumed", func(t *testing.T) {
		e, h := backupEnv(t)
		h.hang[deployhelper.OpBackup] = true
		_, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
		if err == nil || !strings.Contains(err.Error(), "outcome unknown, query ") {
			t.Fatalf("err = %v", err)
		}
		rows := backupRows(t, e)
		if len(rows) != 1 || rows[0].Outcome != catalog.OutcomeInterrupted || !strings.Contains(rows[0].Error, rows[0].OperationID) {
			t.Fatalf("rows = %+v", rows)
		}
	})
	t.Run("the set was made but the service did not start", func(t *testing.T) {
		e, h := backupEnv(t)
		h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
			if req.Op != deployhelper.OpBackup {
				return deployhelper.Response{}, nil, false
			}
			h.mu.Lock()
			h.active = false
			h.mu.Unlock()
			resp := helperReply(req)
			resp.State, resp.Code, resp.Message = deployhelper.StateFailed, deployhelper.CodeFailed, "backup 20261101T120001Z-00000001 was made, but starting the service again failed"
			resp.Backup = &deployhelper.BackupResult{BackupID: "20261101T120001Z-00000001", ManifestSHA256: strings.Repeat("c", 64), Size: 10}
			h.ledger[req.OperationID] = resp
			return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}, true
		}
		res, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
		if err == nil || res.BackupID != "20261101T120001Z-00000001" {
			t.Fatalf("res = %+v, %v", res, err)
		}
		rows := backupRows(t, e)
		if len(rows) != 1 || rows[0].Outcome != catalog.OutcomeSucceeded || rows[0].BackupID != res.BackupID || !strings.Contains(rows[0].Error, "starting the service again failed") {
			t.Fatalf("rows = %+v", rows)
		}
		// The runtime says it is down, so LCM does not keep calling it running.
		if e.state("app") != catalog.StateFailed {
			t.Errorf("state %s", e.state("app"))
		}
	})
}

func TestTypedBackupPlanAndAvailability(t *testing.T) {
	e, h := backupEnv(t)
	p, err := e.mgr.Plan(e.ctx, "app", catalog.ActionBackup, catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, m := range p.StepMessages {
		codes = append(codes, m.Code)
	}
	if !slices.Equal(codes, []string{"step.lock", "step.helperBackup", "step.helperPrune"}) || p.StepMessages[1].Params["mode"] != "stopped" ||
		!strings.Contains(p.StepMessages[1].Params["sources"], "files media, sqlite app.sqlite, command bin/dump") {
		t.Fatalf("plan = %+v", p.StepMessages)
	}

	// No set yet: restore cannot plan.
	if _, err := e.mgr.Plan(e.ctx, "app", catalog.ActionRestore, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrUnknownBackup) {
		t.Fatalf("restore plan without a set = %v", err)
	}
	res, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.mgr.Plan(e.ctx, "app", catalog.ActionRestore, catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	codes = nil
	for _, m := range p.StepMessages {
		codes = append(codes, m.Code)
	}
	want := []string{"step.lock", "step.restoreVerify", "step.restoreStop", "step.restorePreRestore", "step.restoreApply", "step.restoreStart", "step.restoreCheck"}
	if !slices.Equal(codes, want) || p.StepMessages[1].Params["backup"] != res.BackupID || p.From != p.To {
		t.Fatalf("plan = %v %+v", codes, p)
	}
	// The command output cannot be restored, so it is not listed.
	if got := p.StepMessages[3].Params["sources"]; got != "files media, sqlite app.sqlite" {
		t.Errorf("sources %q", got)
	}

	rec, _ := e.cat.Get(context.Background(), "app")
	for _, a := range catalog.AvailableActions(rec, nil) {
		if (a.Action == catalog.ActionBackup || a.Action == catalog.ActionRestore) && !a.Allowed {
			t.Errorf("%+v", a)
		}
	}
	for _, a := range catalog.AvailableActions(rec, errors.New("no helper")) {
		if (a.Action == catalog.ActionBackup || a.Action == catalog.ActionRestore) && a.Reason != catalog.ReasonHelperUnavailable {
			t.Errorf("%+v", a)
		}
	}
	// Restore is info for the guard and refused in groups.
	if info := catalog.ActionInfo(catalog.ActionRestore); info.Risk != resource.RiskDisruptive {
		t.Errorf("risk %s", info.Risk)
	}
	if _, err := e.mgr.PlanGroup(context.Background(), catalog.GroupRequest{Labels: map[string]string{"tier": "web"}, Action: catalog.ActionRestore}); !errors.Is(err, catalog.ErrPrecondition) {
		t.Errorf("group restore = %v", err)
	}
	_ = h
}

func TestRestore(t *testing.T) {
	e, h := backupEnv(t)
	first, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := len(h.Requests())

	// Without an ID the newest set is restored; the checksum recorded for
	// it is sent along.
	res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := opsAfter(h, n); !slices.Equal(got, []deployhelper.Op{deployhelper.OpRestore, deployhelper.OpBackups, deployhelper.OpStatus}) {
		t.Fatalf("ops = %v", got)
	}
	req := h.Requests()[n]
	row, _ := e.cat.Backup(context.Background(), "app", second.BackupID)
	if req.BackupID != second.BackupID || req.ManifestSHA256 != row.ManifestSHA256 || res.BackupID != second.BackupID || !strings.HasPrefix(res.PreRestoreID, "pre-restore-") {
		t.Fatalf("request %+v result %+v", req, res)
	}
	if e.state("app") != catalog.StateRunning || res.From != catalog.StateRunning || res.To != catalog.StateRunning {
		t.Errorf("state %s result %+v", e.state("app"), res)
	}
	pre, err := e.cat.Backup(context.Background(), "app", res.PreRestoreID)
	if err != nil || pre.Kind != catalog.BackupKindPreRestore || pre.Size != 2048 || !pre.Restorable() || len(pre.Sources) != 3 {
		t.Fatalf("pre-restore row = %+v, %v", pre, err)
	}
	var restoreRows int
	for _, r := range backupRows(t, e) {
		if r.Kind == catalog.BackupKindRestore {
			restoreRows++
			if r.BackupID != second.BackupID || r.Outcome != catalog.OutcomeSucceeded || r.OperationID != req.OperationID {
				t.Errorf("restore row %+v", r)
			}
		}
	}
	if restoreRows != 1 {
		t.Errorf("%d restore rows", restoreRows)
	}

	// An explicit ID, here the first set and then the pre-restore set (the
	// undo of a restore).
	if res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: first.BackupID}); err != nil || res.BackupID != first.BackupID {
		t.Fatalf("restore first = %+v, %v", res, err)
	}
	if res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: pre.BackupID}); err != nil || res.BackupID != pre.BackupID {
		t.Fatalf("restore pre-restore = %+v, %v", res, err)
	}

	// Unknown, malformed and removed sets never reach the helper.
	before := len(h.Requests())
	for _, id := range []string{"20200101T000000Z-00000000", "latest", "../x"} {
		if _, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: id}); !errors.Is(err, catalog.ErrUnknownBackup) || !errors.Is(err, catalog.ErrPrecondition) {
			t.Errorf("restore %q = %v", id, err)
		}
	}
	if err := e.cat.MarkBackupsPruned(context.Background(), "app", []string{first.BackupID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: first.BackupID}); !errors.Is(err, catalog.ErrUnknownBackup) {
		t.Errorf("restore of a removed set = %v", err)
	}
	if len(h.Requests()) != before {
		t.Errorf("the helper was called: %v", opsAfter(h, before))
	}

	// The audit trail names the sets.
	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: "app", Kinds: []string{catalog.KindAction}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, en := range page.Entries {
		if en.Data["action"] == catalog.ActionRestore && en.Data["backupId"] == second.BackupID && en.Data["preRestoreId"] == res.PreRestoreID {
			found = true
		}
	}
	if !found {
		t.Errorf("timeline = %+v", page.Entries)
	}
}

func TestRestoreIsUndoneWhenTheServiceDoesNotStart(t *testing.T) {
	e, h := backupEnv(t)
	set, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := len(h.Requests())
	// The restored service stays down; the undo brings it back.
	h.restoreActive = []bool{false, true}
	res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrRestoreUndone) || !strings.Contains(err.Error(), "the previous data was put back from pre-restore-") {
		t.Fatalf("err = %v", err)
	}
	if got := opsAfter(h, n); !slices.Equal(got, []deployhelper.Op{deployhelper.OpRestore, deployhelper.OpBackups, deployhelper.OpStatus, deployhelper.OpRestore, deployhelper.OpBackups, deployhelper.OpStatus}) {
		t.Fatalf("ops = %v", got)
	}
	reqs := h.Requests()
	if first, undo := reqs[n], reqs[n+3]; first.BackupID != set.BackupID || undo.BackupID != res.PreRestoreID {
		t.Fatalf("first %+v undo %+v", first, undo)
	}
	if e.state("app") != catalog.StateRunning {
		t.Errorf("state %s", e.state("app"))
	}
	// Both restores and both pre-restore sets are recorded.
	kinds := map[string]int{}
	for _, r := range backupRows(t, e) {
		kinds[r.Kind]++
	}
	if kinds[catalog.BackupKindRestore] != 2 || kinds[catalog.BackupKindPreRestore] != 2 {
		t.Errorf("rows by kind: %v", kinds)
	}

	t.Run("the undo fails too", func(t *testing.T) {
		e, h := backupEnv(t)
		if _, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{}); err != nil {
			t.Fatal(err)
		}
		h.restoreActive = []bool{false}
		h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
			if req.Op == deployhelper.OpRestore && strings.HasPrefix(req.BackupID, "pre-restore-") {
				resp := helperReply(req)
				resp.Code, resp.Message = deployhelper.CodeFailed, "the restore failed: disk full"
				return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}, true
			}
			return deployhelper.Response{}, nil, false
		}
		_, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{})
		if !errors.Is(err, catalog.ErrRestoreUndone) || !strings.Contains(err.Error(), "failed: ") || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v", err)
		}
		if e.state("app") != catalog.StateFailed {
			t.Errorf("state %s", e.state("app"))
		}
	})
}

func TestRestoreFailureOfTheHelper(t *testing.T) {
	e, h := backupEnv(t)
	set, err := e.mgr.Backup(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The helper undid the restore itself and says so.
	h.restoreFails = true
	res, err := e.mgr.Restore(e.ctx, "app", catalog.ActionOptions{BackupID: set.BackupID})
	var he *rt.HelperError
	if !errors.As(err, &he) || !strings.Contains(err.Error(), "the previous data was put back") {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasPrefix(res.PreRestoreID, "pre-restore-") || e.state("app") != catalog.StateRunning {
		t.Errorf("res = %+v state %s", res, e.state("app"))
	}
	var failed int
	for _, r := range backupRows(t, e) {
		if r.Kind == catalog.BackupKindRestore && r.Outcome == catalog.OutcomeFailed && strings.Contains(r.Error, "disk full") {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("rows = %+v", backupRows(t, e))
	}
}

func TestInterruptedBackupsAreClosed(t *testing.T) {
	e, _ := backupEnv(t)
	id, err := e.cat.BeginBackup(context.Background(), catalog.BackupRecord{Resource: "app", Kind: catalog.BackupKindBackup, OperationID: "00000000-0000-4000-8000-000000000042", Actor: alice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Reconcile(e.ctx); err != nil {
		t.Fatal(err)
	}
	rows := backupRows(t, e)
	if len(rows) != 1 || rows[0].ID != id || rows[0].Outcome != catalog.OutcomeInterrupted || !strings.Contains(rows[0].Error, "query 00000000-0000-4000-8000-000000000042") {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPruneBackupRows(t *testing.T) {
	e, _ := backupEnv(t)
	ctx := context.Background()
	add := func(kind, outcome, id string, pruned bool) {
		t.Helper()
		if _, err := e.cat.AddBackup(ctx, catalog.BackupRecord{Resource: "app", Kind: kind, BackupID: id, Outcome: outcome, Actor: alice}); err != nil {
			t.Fatal(err)
		}
		if pruned {
			if err := e.cat.MarkBackupsPruned(ctx, "app", []string{id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(catalog.BackupKindBackup, catalog.OutcomeSucceeded, "20261101T120001Z-00000001", false) // still on disk
	add(catalog.BackupKindBackup, catalog.OutcomeSucceeded, "20261101T120002Z-00000002", true)  // removed
	add(catalog.BackupKindBackup, catalog.OutcomeFailed, "", false)
	add(catalog.BackupKindRestore, catalog.OutcomeSucceeded, "20261101T120001Z-00000001", false)
	running, err := e.cat.BeginBackup(ctx, catalog.BackupRecord{Resource: "app", Kind: catalog.BackupKindBackup, Actor: alice})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.cat.PruneBackupRows(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("young rows: %d %v", n, err)
	}
	e.clk.Advance(2 * time.Hour)
	if n, err := e.cat.PruneBackupRows(ctx, time.Hour); err != nil || n != 3 {
		t.Fatalf("old rows: %d %v", n, err)
	}
	var left []string
	for _, r := range backupRows(t, e) {
		left = append(left, fmt.Sprintf("%s/%s/%d", r.Kind, r.Outcome, r.ID))
	}
	if len(left) != 2 || !slices.Contains(left, fmt.Sprintf("backup/running/%d", running)) {
		t.Errorf("left %v", left)
	}
}
