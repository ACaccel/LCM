package modules_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestModuleMaintenanceGuard(t *testing.T) {
	db := storetest.Open(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
	log := audit.New(db)
	gate := maintenance.New(db)
	s, err := modules.New(db, log, nil, modules.WithGate(gate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, doc("m", "original"), 0); err != nil {
		t.Fatal(err)
	}
	if err := gate.Exclusive(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES('m','local','machine','local','managed',1,0,0)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO machine_maintenance(host,resource_id,state,entered_at,body) VALUES('local','m','active',0,'{}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []*maintenance.Gate{gate, maintenance.New(db)} {
		guarded, err := modules.New(db, log, nil, modules.WithGate(g))
		if err != nil {
			t.Fatal(err)
		}
		for _, edit := range []struct {
			name, param string
			expected    int
		}{
			{"new", "create", 0},
			{"m", "updated", 1},
			{"m", "original", 1},
		} {
			res, err := guarded.Put(ctx, doc(edit.name, edit.param), edit.expected)
			if !errors.Is(err, maintenance.ErrActive) || res != (modules.PutResult{}) {
				t.Fatalf("put during maintenance = %+v, %v", res, err)
			}
		}
	}
	versions, err := s.Versions(ctx, "m")
	if err != nil || len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versions after denied update: %+v, %v", versions, err)
	}
	rec, err := s.Get(ctx, "m")
	if err != nil || rec.Spec.Spec.Params[0].Name != "original" {
		t.Fatalf("document after denied update: %+v, %v", rec, err)
	}
	if _, err := s.Get(ctx, "new"); !errors.Is(err, modules.ErrNotFound) {
		t.Fatalf("denied creation stored a module: %v", err)
	}
	if _, err := s.ResolveModule(ctx, "m"); err != nil {
		t.Fatalf("read during maintenance: %v", err)
	}
	entries, err := log.List(ctx, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("denied writes changed audit: %+v, %v", entries, err)
	}
	expected := 1
	for _, opts := range [][]modules.Option{nil, {modules.WithGate(nil)}} {
		ungated, err := modules.New(db, log, nil, opts...)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ungated.Put(ctx, doc("m", "allowed"), expected)
		if err != nil || res.Version != 2 {
			t.Fatalf("ungated update = %+v, %v", res, err)
		}
		expected = res.Version
		// Both constructor forms retain the existing identical-save behavior.
		res, err = ungated.Put(ctx, doc("m", "allowed"), 2)
		if err != nil || res.Changed {
			t.Fatalf("ungated identical save = %+v, %v", res, err)
		}
	}
}

func TestModuleWriteMaintenanceLease(t *testing.T) {
	db := storetest.Open(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
	gate := maintenance.New(db)
	audited := 0
	probeCtx, cancel := context.WithCancel(ctx)
	cancel()
	log := audit.New(db, audit.WithClock(func() time.Time {
		// Audit happens after the document commit and must still hold the lease.
		// A cancelled context keeps a missing lease from nesting writer transactions.
		if err := gate.Exclusive(probeCtx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
			t.Errorf("maintenance entered before audit completed: %v", err)
		}
		audited++
		return epoch
	}))
	s, err := modules.New(db, log, clock.NewFake(epoch), modules.WithGate(gate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, doc("lease", "first"), 0); err != nil {
		t.Fatal(err)
	}
	leased, release, err := gate.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := s.Put(leased, doc("lease", "second"), 1); err != nil {
		t.Fatalf("nested put: %v", err)
	}
	if err := gate.Exclusive(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("nested put released the outer lease: %v", err)
	}
	release()
	if err := gate.Exclusive(ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("put leaked its lease: %v", err)
	}
	if audited != 2 {
		t.Fatalf("audit calls = %d, want 2", audited)
	}
}
