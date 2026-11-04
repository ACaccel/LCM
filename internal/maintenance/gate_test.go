package maintenance_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestGateSerializesDurableMaintenance(t *testing.T) {
	db := storetest.Open(t)
	g := maintenance.New(db)
	ctx := context.Background()
	lease, release, err := g.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, nestedRelease, err := g.Acquire(lease)
	if err != nil {
		t.Fatal(err)
	}
	nestedRelease()
	if err = g.Exclusive(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("active writer: %v", err)
	}
	release()
	err = g.Exclusive(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES('m','local','machine','local','managed',1,0,0)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO machine_maintenance(host,resource_id,state,entered_at,body) VALUES('local','m','active',0,'{}')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range []*maintenance.Gate{g, maintenance.New(db)} {
		if _, _, err := gate.Acquire(ctx); !errors.Is(err, maintenance.ErrActive) {
			t.Fatalf("gate lost after restart: %v", err)
		}
	}
	if _, _, err := g.Acquire(lease); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("expired lease reused: %v", err)
	}
}
