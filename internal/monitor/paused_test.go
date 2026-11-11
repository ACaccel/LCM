package monitor

import (
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

func paused(d Def) Def {
	d.Paused, d.PauseReason = true, "instance b is stopped"
	return d
}

func onInstance(name string) func(*model.Check) {
	return func(c *model.Check) { c.Instance = name }
}

// A check of an instance an operator stopped is paused: it does not run,
// does not count towards the service's health and is unknown until it runs
// again after the instance is started.
func TestPausedChecks(t *testing.T) {
	h := newHarness(t)
	a, b := fakeDef("app", "a", onInstance("a")), fakeDef("app", "b", onInstance("b"))
	h.sync(a, b)
	h.step(0)
	if h.health("app") != HealthHealthy || h.check("app", "b").Instance != "b" {
		t.Fatalf("health = %s", h.health("app"))
	}

	h.fail("app/b")
	h.sync(a, paused(b))
	runs := h.callCount("app/b")
	h.step(testInterval)
	h.step(testInterval)
	if h.callCount("app/b") != runs {
		t.Fatal("a paused check ran")
	}
	// A paused critical check neither fails nor holds back the health.
	if h.health("app") != HealthHealthy {
		t.Fatalf("health with b paused = %s", h.health("app"))
	}
	c := h.check("app", "b")
	if !c.Paused || c.Status != StatusUnknown || c.Message != "instance b is stopped" {
		t.Fatalf("paused check = %+v", c)
	}

	// Resumed, it runs again and counts from its first result on.
	h.pass("app/b")
	h.sync(a, b)
	if c := h.check("app", "b"); c.Paused || c.Status != StatusUnknown {
		t.Fatalf("resumed check = %+v", c)
	}
	h.step(0)
	if h.callCount("app/b") == runs || h.check("app", "b").Status != StatusUp || h.health("app") != HealthHealthy {
		t.Fatalf("after resume: %+v %s", h.check("app", "b"), h.health("app"))
	}
}
