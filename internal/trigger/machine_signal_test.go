package trigger

import (
	"testing"

	"github.com/ACaccel/lcm/internal/signal"
)

func TestMachineSignalsDoNotWidenServiceWildcard(t *testing.T) {
	p := SignalProps{Severities: []string{"warning"}}
	if matchSignal(p, signal.Signal{Kind: "machine.config.conflict", Severity: "warning"}) {
		t.Fatal("legacy wildcard received a machine signal")
	}
	if !matchSignal(p, signal.Signal{Kind: signal.KindAlert, Severity: "warning"}) {
		t.Fatal("legacy alert behavior changed")
	}
}
