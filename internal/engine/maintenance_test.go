package engine_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
)

func enterMaintenance(t *testing.T, h *enginetest.Harness) {
	t.Helper()
	writeSQL(t, h, `INSERT INTO machines(id,name,version,created_at,updated_at) VALUES('m','local',1,0,0)`)
	writeSQL(t, h, `INSERT INTO machine_maintenance(host,machine_id,state,entered_at,body) VALUES('local','m','active',0,'{}')`)
}

func TestPolicyMaintenanceGuard(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "without gate"
		if guarded {
			name = "shared gate"
		}
		t.Run(name, func(t *testing.T) {
			h := enginetest.New(t)
			e := h.Open(func(c *engine.Config) {
				if guarded {
					c.Gate = maintenance.New(h.DB)
				}
			})
			doc := policy("maint", suspendPolicy)
			h.Deploy(e, doc)
			h.Deploy(e, policy("remove", "  nodes: [{ id: start, type: trigger.manual }]\n"))
			id := h.Start(e, "maint", map[string]any{"key": "k", "resumeIn": "0s"})
			h.Events.WaitFor(t, "suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
			if err := e.SetPaused(h.Ctx, "maint", true); err != nil {
				t.Fatal(err)
			}
			enterMaintenance(t, h)
			check := func(action string, err error) {
				t.Helper()
				if guarded && !errors.Is(err, maintenance.ErrActive) {
					t.Fatalf("%s: %v, want maintenance.ErrActive", action, err)
				}
				if !guarded && err != nil {
					t.Fatalf("%s without gate: %v", action, err)
				}
			}
			v, err := e.Deploy(h.Ctx, []byte(strings.Replace(doc, "timeout: 2h", "timeout: 3h", 1)))
			check("deploy update", err)
			if guarded && v != 0 || !guarded && v != 2 {
				t.Fatalf("updated version = %d", v)
			}
			_, err = e.Deploy(h.Ctx, []byte(policy("new", "  nodes: [{ id: start, type: trigger.manual }]\n")))
			check("deploy create", err)
			check("undeploy", e.Undeploy(h.Ctx, "remove"))
			check("unpause", e.SetPaused(h.Ctx, "maint", false))
			newID, err := e.Start(h.Ctx, "maint", "start", map[string]any{"key": "new", "resumeIn": "0s"}, engine.ModeLive)
			check("start", err)
			if guarded && newID != "" {
				t.Fatalf("denied start returned run ID %q", newID)
			}
			check("resume", e.Resume(h.Ctx, id, "wait", "k", "approved"))
			if !guarded {
				if view := h.Wait(e, id); view.Status != engine.RunSucceeded {
					t.Fatalf("ungated resume: %s", view.Status)
				}
				return
			}
			versions, err := e.PolicyVersions(h.Ctx, "maint")
			if err != nil || len(versions) != 1 || versions[0].Version != 1 {
				t.Fatalf("versions after denial: %+v, %v", versions, err)
			}
			if plan, ok := e.Plan("maint"); !ok || plan.Version() != 1 {
				t.Fatal("denied deploy changed the in-memory plan")
			}
			if _, ok := e.Plan("remove"); !ok {
				t.Fatal("denied undeploy removed the plan")
			}
			if _, ok := e.Plan("new"); ok {
				t.Fatal("denied deploy created a plan")
			}
			var paused bool
			if err := h.DB.Reader().QueryRow(`SELECT paused FROM policy_deployments WHERE name='maint'`).Scan(&paused); err != nil || !paused {
				t.Fatalf("stored paused = %v, %v", paused, err)
			}
			var status string
			var payload sql.NullString
			if err := h.DB.Reader().QueryRow(`SELECT status,resume_payload FROM run_nodes WHERE run_id=? AND node_id='wait'`, id).Scan(&status, &payload); err != nil || status != "suspended" || payload.Valid {
				t.Fatalf("checkpoint after denied resume = %s, %+v, %v", status, payload, err)
			}
			if _, err := e.Validate(h.Ctx, []byte(doc)); err != nil {
				t.Fatalf("validate during maintenance: %v", err)
			}
			if view, err := e.Run(h.Ctx, id); err != nil || view.Status != engine.RunSuspended {
				t.Fatalf("read during maintenance: %+v, %v", view, err)
			}
			if err := e.SetPaused(h.Ctx, "maint", true); err != nil {
				t.Fatalf("pause during maintenance: %v", err)
			}
			if err := e.Cancel(h.Ctx, id); err != nil {
				t.Fatalf("cancel during maintenance: %v", err)
			}
			if view := h.Wait(e, id); view.Status != engine.RunCancelled {
				t.Fatalf("cancel status: %s", view.Status)
			}
		})
	}
}

type maintenanceResolver func(context.Context, string) (*model.Module, error)

func (f maintenanceResolver) ResolveModule(ctx context.Context, name string) (*model.Module, error) {
	return f(ctx, name)
}

func TestDeployMaintenanceLease(t *testing.T) {
	h := enginetest.New(t)
	gate := maintenance.New(h.DB)
	h.AddModule(module("m", `
  nodes:
    - { id: in, type: module.input, next: { out: [out] } }
    - { id: out, type: module.output, props: { port: ok } }
`))
	var wantCtx context.Context
	probeCtx, cancel := context.WithCancel(h.Ctx)
	cancel()
	audited := 0
	e := h.Open(func(c *engine.Config) {
		c.Gate = gate
		c.Audit = audit.New(h.DB, audit.WithClock(func() time.Time {
			// A cancelled context avoids a nested writer transaction if the lease regresses.
			if err := gate.Exclusive(probeCtx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
				t.Errorf("maintenance entered before policy audit completed: %v", err)
			}
			audited++
			return enginetest.Epoch
		}))
		c.Modules = maintenanceResolver(func(ctx context.Context, name string) (*model.Module, error) {
			if wantCtx != nil && ctx != wantCtx {
				t.Fatal("deploy did not preserve the caller's lease context")
			}
			nested, release, err := gate.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if nested != ctx {
				t.Fatal("module resolution did not receive the deploy lease")
			}
			if err := gate.Exclusive(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
				t.Fatalf("maintenance entered during compilation: %v", err)
			}
			return h.Modules.ResolveModule(ctx, name)
		})
	})
	doc := []byte(policy("nested", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: m } }
`))
	if _, err := e.Deploy(h.Ctx, doc); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := gate.Acquire(h.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	wantCtx = ctx
	if _, err := e.Deploy(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if err := e.SetPaused(ctx, "nested", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Undeploy(ctx, "nested"); err != nil {
		t.Fatal(err)
	}
	if err := gate.Exclusive(h.Ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("nested operation released the caller's lease: %v", err)
	}
	release()
	if err := gate.Exclusive(h.Ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("deploy leaked its lease: %v", err)
	}
	if audited != 4 {
		t.Fatalf("audit calls = %d, want 4", audited)
	}
}

func TestRecoverDeferredByDurableMaintenance(t *testing.T) {
	h := enginetest.New(t)
	e1 := h.Open()
	h.Deploy(e1, policy("recover-maint", suspendPolicy))
	keyID := h.Start(e1, "recover-maint", map[string]any{"key": "k", "resumeIn": "0s"})
	timerID := h.Start(e1, "recover-maint", map[string]any{"key": "", "resumeIn": "10m"})
	storedID := h.Start(e1, "recover-maint", map[string]any{"key": "stored", "resumeIn": "0s"})
	for _, id := range []string{keyID, timerID, storedID} {
		h.Events.WaitFor(t, "suspended", enginetest.RunEvent(engine.EventRunSuspended, id))
	}
	if err := e1.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, h, `UPDATE run_nodes SET status='resumed', resume_payload='"stored"' WHERE run_id=? AND node_id='wait'`, storedID)
	enterMaintenance(t, h)
	gate := maintenance.New(h.DB)
	e2 := h.Open(func(c *engine.Config) { c.Gate = gate })
	for range 2 {
		err := e2.Recover(h.Ctx)
		if !errors.Is(err, engine.ErrRecoveryDeferred) || !errors.Is(err, maintenance.ErrActive) {
			t.Fatalf("recover during maintenance: %v", err)
		}
	}
	if len(e2.Plans()) != 0 || h.Clock.Pending() != 0 {
		t.Fatal("deferred recovery installed plans or armed timers")
	}
	h.Clock.Advance(15 * time.Minute)
	for _, id := range []string{keyID, timerID, storedID} {
		if status := storedStatus(t, h, id); status != "suspended" {
			t.Fatalf("deferred run %s changed status: %s", id, status)
		}
	}
	if h.Calls("wait") != 3 || h.Calls("after") != 0 {
		t.Fatal("deferred recovery executed nodes")
	}
	if err := gate.Exclusive(h.Ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(h.Ctx, `DELETE FROM machine_maintenance WHERE host='local'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e2.Recover(h.Ctx); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	if err := e2.Recover(h.Ctx); !errors.Is(err, engine.ErrAlreadyRecovered) {
		t.Fatalf("recover after successful retry: %v", err)
	}
	if err := e2.Resume(h.Ctx, keyID, "wait", "k", "key"); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(0)
	for _, id := range []string{keyID, timerID, storedID} {
		if view := h.Wait(e2, id); view.Status != engine.RunSucceeded {
			t.Fatalf("recovered run %s: %s (%s)", id, view.Status, view.Error)
		}
	}
}
