package trigger

import (
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/signal"
)

func TestConfigSyncSignalsDoNotWidenWildcards(t *testing.T) {
	p := SignalProps{Severities: []string{"warning"}}
	if matchSignal(p, signal.Signal{Kind: "resource.config.conflict", Severity: "warning"}) {
		t.Fatal("a filter without kinds received a config sync signal")
	}
	if !matchSignal(p, signal.Signal{Kind: signal.KindAlert, Severity: "warning"}) {
		t.Fatal("alert behavior changed")
	}
}

func TestSignalTypesFilter(t *testing.T) {
	p := SignalProps{Kinds: []string{string(signal.KindHealth)}, Types: []string{"router", "machine"}}
	for typ, want := range map[string]bool{"router": true, "machine": true, "service": false, "": false} {
		if got := matchSignal(p, signal.Signal{Kind: signal.KindHealth, Resource: "r", ResourceType: typ}); got != want {
			t.Errorf("type %q: match = %v, want %v", typ, got, want)
		}
	}
	if err := validateSignal(SignalProps{Types: []string{"printer"}}); err == nil || !strings.Contains(err.Error(), "types") {
		t.Fatalf("unknown type accepted: %v", err)
	}
	if err := validateSignal(SignalProps{Types: []string{"router"}}); err != nil {
		t.Fatalf("types alone: %v", err)
	}
}
