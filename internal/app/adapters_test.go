package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/machine/power"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestLockAdapterMapsBusyToCatalogError(t *testing.T) {
	ctx := context.Background()
	l := lockAdapter{m: lock.New(storetest.Open(t))}

	release, err := l.Acquire(ctx, "service:web", "run-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.Acquire(ctx, "service:web", "run-2", time.Hour)
	var busy *lock.BusyError
	if !errors.Is(err, catalog.ErrLockHeld) || !errors.Is(err, lock.ErrHeld) || !errors.As(err, &busy) || busy.Holder.Owner != "run-1" {
		t.Fatalf("busy lock error = %v", err)
	}

	// The same owner extends its lock, which keeps retried actions idempotent.
	if _, err := l.Acquire(ctx, "service:web", "run-1", time.Hour); err != nil {
		t.Fatalf("re-acquire by owner: %v", err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "service:web", "run-2", time.Hour); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	// Errors other than "held" must not look like a busy service.
	_, err = l.Acquire(ctx, "bad name!", "run-1", time.Hour)
	if err == nil || errors.Is(err, catalog.ErrLockHeld) || !errors.Is(err, lock.ErrInvalid) {
		t.Fatalf("invalid name error = %v", err)
	}
}

func TestMonitorRunnerTranslatesResults(t *testing.T) {
	fake := &execx.Fake{}
	fake.Reply("is-active up", "active\n", 0).Reply("is-active down", "inactive\n", 3)
	fake.On(func(c execx.Cmd) bool { return c.Argv[0] == "/opt/bin/systemctl" && c.Argv[1] == "boom" },
		func(execx.Cmd) (execx.Result, error) { return execx.Result{ExitCode: -1}, errors.New("not started") })
	r := monitorRunner{run: fake, binaries: map[string]string{"systemctl": "/opt/bin/systemctl"}}
	ctx := context.Background()

	res, err := r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"is-active", "up"}})
	if err != nil || res.ExitCode != 0 || res.Stdout != "active\n" {
		t.Fatalf("success: %+v, %v", res, err)
	}
	res, err = r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"is-active", "down"}})
	if err != nil || res.ExitCode != 3 || res.Stdout != "inactive\n" || res.Stderr != "inactive\n" {
		t.Fatalf("non-zero exit must be a result, got %+v, %v", res, err)
	}
	if _, err = r.Run(ctx, monitor.Command{Name: "systemctl", Args: []string{"boom"}}); err == nil {
		t.Fatal("a command that could not run must be an error")
	}

	calls := fake.Calls()
	if calls[0].Argv[0] != "/opt/bin/systemctl" {
		t.Errorf("configured binary not used: %v", calls[0].Argv)
	}
	_, _ = r.Run(ctx, monitor.Command{Name: "other", Args: []string{"x"}})
	if last := fake.Calls()[len(fake.Calls())-1]; last.Argv[0] != "other" {
		t.Errorf("unmapped program renamed: %v", last.Argv)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Run(cancelled, monitor.Command{Name: "x"}); err == nil {
		t.Error("canceled context must be an error")
	}
}

func TestMonitorDefsSkipRetiredAndCarryLabels(t *testing.T) {
	rec := func(name, typ, phase string, checks ...string) resource.Record {
		doc := &model.Resource{}
		doc.Metadata = model.Metadata{Name: name, Labels: map[string]string{"tier": "web"}}
		for _, c := range checks {
			doc.Spec.Checks = append(doc.Spec.Checks, model.Check{Name: c, Type: "http"})
		}
		return resource.Record{Name: name, Type: typ, Phase: phase, Document: doc}
	}
	recs := []resource.Record{
		rec("a", model.ResourceService, resource.PhaseManaged, "one", "two"),
		rec("b", model.ResourceService, resource.PhaseRetired, "one"),
		rec("c", model.ResourceService, resource.PhaseManaged),
		rec("host", model.ResourceMachine, resource.PhaseManaged, "ping"),
		{Name: "d", Type: model.ResourceService, Phase: resource.PhaseManaged},
	}
	defs := monitorDefs(recs)
	if len(defs) != 3 || defs[0].ID() != "a/one" || defs[1].ID() != "a/two" || defs[0].Labels["tier"] != "web" ||
		defs[0].ResourceType != model.ResourceService || defs[2].ID() != "host/ping" || defs[2].ResourceType != model.ResourceMachine {
		t.Fatalf("defs = %+v", defs)
	}
}

// Migration warnings are repeated only while the named document is still at
// the version the warning was recorded for.
func TestMigrationWarningsStopOnceFixed(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	doc := func(kind, name string) string {
		return `{"apiVersion":"lcm/v2","kind":"` + kind + `","metadata":{"name":"` + name + `"},"spec":{"nodes":[]}}`
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO policy_versions (name, version, document, created_at, created_by_kind, created_by_id, created_by_name) VALUES (?, ?, ?, 0, 'system', 'cli', 'cli')`, []any{"stale", 1, doc("Policy", "stale")}},
			{`INSERT INTO policy_versions (name, version, document, created_at, created_by_kind, created_by_id, created_by_name) VALUES (?, ?, ?, 0, 'system', 'cli', 'cli')`, []any{"fixed", 1, doc("Policy", "fixed")}},
			{`INSERT INTO policy_versions (name, version, document, created_at, created_by_kind, created_by_id, created_by_name) VALUES (?, ?, ?, 0, 'system', 'cli', 'cli')`, []any{"fixed", 2, doc("Policy", "fixed")}},
			{`INSERT INTO module_versions (name, version, document, created_at, created_by_kind, created_by_id, created_by_name) VALUES (?, ?, ?, 0, 'system', 'cli', 'cli')`, []any{"mod", 1, doc("Module", "mod")}},
		} {
			if _, err := tx.ExecContext(ctx, q.query, q.args...); err != nil {
				return err
			}
		}
		for _, w := range [][]any{{"Policy", "stale", 1}, {"Policy", "fixed", 1}, {"Policy", "gone", 1}, {"Module", "mod", 1}} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO migration_warnings (migration, kind, name, version, message, created_at) VALUES (14, ?, ?, ?, 'needs work', 0)`, w...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logMigrationWarnings(ctx, db, slog.New(slog.NewTextHandler(&buf, nil)))
	out := buf.String()
	if strings.Count(out, "converted document needs review") != 2 || !strings.Contains(out, "name=stale") || !strings.Contains(out, "name=mod") ||
		strings.Contains(out, "name=fixed") || strings.Contains(out, "name=gone") {
		t.Fatalf("logged:\n%s", out)
	}
}

// The helper's answer that it has no receipt of a request reaches the
// machine type as ErrReceiptMissing, and only for a query: it proves that a
// pending request was never prepared and so never submitted.
func TestMachinePowerMapsMissingReceipt(t *testing.T) {
	runner := (&execx.Fake{}).Reply(power.WrapperPath, `{"version":1,"code":"receipt-missing"}`, 0)
	p := machinePower{client: power.Client{Runner: runner}, runner: runner}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	req := machine.PowerRequest{
		ControllerID: "11111111-1111-4111-8111-111111111111", OperationID: "22222222-2222-4222-8222-222222222222",
		BootID: "33333333-3333-4333-8333-333333333333", Action: "reboot", IssuedAt: now, ValidUntil: now.Add(time.Minute),
	}
	if _, err := p.Query(context.Background(), req); !errors.Is(err, machine.ErrReceiptMissing) {
		t.Fatalf("query: %v", err)
	}
	if _, err := p.call(context.Background(), power.Prepare, req); err == nil || errors.Is(err, machine.ErrReceiptMissing) {
		t.Fatalf("prepare: %v", err)
	}
}
