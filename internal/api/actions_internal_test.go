package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestActionTrackerForgetsOldestFinishedActionsFirst(t *testing.T) {
	tr := newActionTracker(3)
	now := time.Now()

	running := tr.begin(actionDTO{Service: "web", Action: "start"})
	var finished []string
	for i := range 4 {
		a := tr.begin(actionDTO{Service: fmt.Sprintf("svc%d", i), Action: "stop"})
		tr.finish(a.ID, now, nil, nil)
		finished = append(finished, a.ID)
	}

	// The still-running action survives; the oldest finished ones are gone.
	if _, ok := tr.get(running.ID); !ok {
		t.Fatal("running action was forgotten")
	}
	for i, id := range finished {
		_, ok := tr.get(id)
		if want := i >= 2; ok != want {
			t.Errorf("finished action %d kept = %v, want %v", i, ok, want)
		}
	}
	if !tr.runningOn("web") || tr.runningOn("svc3") {
		t.Errorf("runningOn: web=%v svc3=%v", tr.runningOn("web"), tr.runningOn("svc3"))
	}
}

func TestActionTrackerRecordsOutcome(t *testing.T) {
	tr := newActionTracker(10)
	a := tr.begin(actionDTO{Service: "web", Action: "restart"})
	if a.Status != actionRunning || !strings.HasPrefix(a.ID, "act_") {
		t.Fatalf("begun action = %+v", a)
	}
	done := tr.finish(a.ID, time.Now(), nil, errors.New("catalog: restart command: exit status 1"))
	if done.Status != actionFailed || done.Error != "restart command: exit status 1" || done.FinishedAt == nil {
		t.Fatalf("failed action = %+v", done)
	}
	got, _ := tr.get(a.ID)
	if got.Status != actionFailed {
		t.Fatalf("stored action = %+v", got)
	}
	if tr.runningOn("web") {
		t.Error("finished action still counts as running")
	}

	long := tr.begin(actionDTO{Service: "db", Action: "backup"})
	done = tr.finish(long.ID, time.Now(), nil, errors.New(strings.Repeat("x", 5000)))
	if len(done.Error) > maxActionError+3 {
		t.Errorf("error text not capped: %d bytes", len(done.Error))
	}
}
