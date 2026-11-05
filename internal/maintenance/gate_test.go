package maintenance_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
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

func TestNilGateAdmitsEverything(t *testing.T) {
	var g *maintenance.Gate
	ctx := context.Background()
	got, release, err := g.Acquire(ctx)
	if err != nil || got != ctx || release == nil {
		t.Fatalf("nil gate: %v %v", got, err)
	}
	release()
}

func TestGateLeasesAreSharedAndReleasedOnce(t *testing.T) {
	g := maintenance.New(storetest.Open(t))
	ctx := context.Background()
	_, releaseA, err := g.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseB, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
	// A repeated release must not give up the other writer's lease.
	releaseA()
	releaseA()
	if err := g.Exclusive(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("maintenance entered while a writer is active: %v", err)
	}
	releaseB()
	if err := g.Exclusive(ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("maintenance after all writers left: %v", err)
	}
}

func TestGateRejectsWritesDuringTransition(t *testing.T) {
	db := storetest.Open(t)
	g, other := maintenance.New(db), maintenance.New(db)
	ctx := context.Background()
	otherLease, releaseOther, err := other.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()
	boom := errors.New("transition failed")
	err = g.Exclusive(ctx, func(*sql.Tx) error {
		if _, _, err := g.Acquire(ctx); !errors.Is(err, maintenance.ErrActive) {
			t.Errorf("write admitted during the transition: %v", err)
		}
		// A lease of another gate does not bypass this one.
		if _, _, err := g.Acquire(otherLease); !errors.Is(err, maintenance.ErrActive) {
			t.Errorf("foreign lease admitted during the transition: %v", err)
		}
		if err := g.Exclusive(ctx, func(*sql.Tx) error { return nil }); !errors.Is(err, maintenance.ErrActive) {
			t.Errorf("nested transition: %v", err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("exclusive: %v", err)
	}
	// The failed transition stored nothing: writes are admitted again.
	_, release, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("write after a failed transition: %v", err)
	}
	release()
}

func TestGateFailedCheckKeepsNoLease(t *testing.T) {
	g := maintenance.New(storetest.Open(t))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, release, err := g.Acquire(cancelled); err == nil || errors.Is(err, maintenance.ErrActive) || release != nil {
		t.Fatalf("acquire with a failing check: %v", err)
	}
	if err := g.Exclusive(context.Background(), func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("lease leaked by a failed check: %v", err)
	}
}

// Leases and transitions race; a transition never runs while a lease is
// held.
func TestGateExcludesConcurrentWriters(t *testing.T) {
	g := maintenance.New(storetest.Open(t))
	ctx := context.Background()
	var held atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				lease, release, err := g.Acquire(ctx)
				if errors.Is(err, maintenance.ErrActive) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				held.Add(1)
				if _, nested, err := g.Acquire(lease); err != nil {
					t.Errorf("nested acquire: %v", err)
				} else {
					nested()
				}
				held.Add(-1)
				release()
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 50 {
				err := g.Exclusive(ctx, func(*sql.Tx) error {
					if n := held.Load(); n != 0 {
						t.Errorf("transition ran with %d leases held", n)
					}
					return nil
				})
				if err != nil && !errors.Is(err, maintenance.ErrActive) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if err := g.Exclusive(ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("lock not released after the race: %v", err)
	}
}
