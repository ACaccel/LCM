package engine_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func echoPolicy(name, node string) string {
	return policy(name, fmt.Sprintf(`
  nodes:
    - { id: start, type: trigger.manual, next: { out: [%s] } }
    - { id: %s, type: test.echo }
`, node, node))
}

func latestVersion(t *testing.T, e *engine.Engine, name string) int {
	t.Helper()
	vs, err := e.PolicyVersions(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return vs[0].Version
}

func TestDeployExpectedVersion(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "a")), 1); !errors.Is(err, engine.ErrPolicyNotFound) {
		t.Fatalf("update of a missing policy: %v", err)
	}
	if v, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "a")), 0); err != nil || v != 1 {
		t.Fatalf("create = %d, %v", v, err)
	}
	if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "b")), 0); !errors.Is(err, engine.ErrPolicyExists) {
		t.Fatalf("second create: %v", err)
	}
	if v, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "b")), 1); err != nil || v != 2 {
		t.Fatalf("update = %d, %v", v, err)
	}
	// A writer still based on version 1 must not replace version 2.
	if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "c")), 1); !errors.Is(err, engine.ErrVersionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if v := latestVersion(t, e, "p"); v != 2 {
		t.Fatalf("latest version = %d after a refused update", v)
	}
	if p, _ := e.Plan("p"); p.Version() != 2 {
		t.Fatalf("deployed plan = v%d", p.Version())
	}
	if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("p", "c")), -2); err == nil {
		t.Fatal("negative expected version accepted")
	}
}

// Concurrent deploys of one policy commit in some order; the plan in
// memory must be the one the last commit stored. The hook holds the first
// commit of every round in the window between its commit and its map
// update until the other writers of the round have committed, so that
// without ordering they overtake it there. With ordering they cannot
// commit meanwhile, and the bounded wait gives up.
func TestConcurrentDeploysKeepTheStoredPlan(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	const writers = 4
	var stores atomic.Int64
	defer engine.SetAfterStore(func() {
		n := stores.Add(1)
		if n%writers != 1 {
			return
		}
		for i := 0; i < 200000 && stores.Load() < n+writers-1; i++ {
			runtime.Gosched()
		}
		for range 1000 {
			runtime.Gosched()
		}
	})()
	for round := range 3 {
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() {
				if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("race", fmt.Sprintf("n%d_%d", round, i))), engine.AnyVersion); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		infos, err := e.Policies(context.Background())
		if err != nil || len(infos) != 1 {
			t.Fatalf("policies = %v, %v", infos, err)
		}
		p, ok := e.Plan("race")
		if !ok || p.Version() != infos[0].DeployedVersion {
			t.Fatalf("round %d: memory has v%d, database deployed v%d", round, p.Version(), infos[0].DeployedVersion)
		}
	}
}

func TestDeploymentAuditIsAtomic(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, echoPolicy("kept", "a"))
	restore := storetest.FailInserts(t, h.DB, "audit_log")

	if _, err := e.Deploy(h.Ctx, []byte(echoPolicy("lost", "a")), 0); err == nil {
		t.Fatal("deploy succeeded without its audit entry")
	}
	if _, err := e.PolicyVersions(context.Background(), "lost"); !errors.Is(err, engine.ErrPolicyNotFound) {
		t.Fatalf("version stored without an audit entry: %v", err)
	}
	if _, ok := e.Plan("lost"); ok {
		t.Fatal("plan deployed without an audit entry")
	}
	if err := e.SetPaused(h.Ctx, "kept", true); err == nil {
		t.Fatal("pause succeeded without its audit entry")
	}
	if err := e.Undeploy(h.Ctx, "kept"); err == nil {
		t.Fatal("undeploy succeeded without its audit entry")
	}
	restore()

	infos, err := e.Policies(context.Background())
	if err != nil || len(infos) != 1 || infos[0].DeployedVersion != 1 || infos[0].Paused {
		t.Fatalf("policies = %+v, %v", infos, err)
	}
	if _, ok := e.Plan("kept"); !ok {
		t.Fatal("undeploy took effect in memory")
	}
	entries, err := audit.New(h.DB).List(context.Background(), 10, 0)
	if err != nil || len(entries) != 1 || entries[0].Action != "policy.deploy" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}
