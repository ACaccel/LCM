package model

import (
	"strings"
	"testing"
)

func TestCheckDefaults(t *testing.T) {
	c := Check{Name: "http", Type: "http"}.WithDefaults()
	if c.Interval != DefaultCheckInterval || c.Timeout != DefaultCheckTimeout || c.Threshold != DefaultCheckThreshold {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if !c.IsCritical() {
		t.Fatal("nil Critical must mean critical")
	}
	f := false
	if (Check{Critical: &f}).IsCritical() {
		t.Fatal("explicit false must be non-critical")
	}
}

func TestValidateChecks(t *testing.T) {
	ok := []Check{{Name: "http", Type: "http"}, {Name: "unit", Type: "systemd", Interval: Duration(MinCheckInterval * 10)}}
	if err := ValidateChecks(ok, "spec.checks"); err != nil {
		t.Fatalf("valid checks rejected: %v", err)
	}

	cases := map[string]struct {
		checks []Check
		path   string
	}{
		"bad name":       {[]Check{{Name: "Bad_Name", Type: "http"}}, "spec.checks[0].name"},
		"duplicate":      {[]Check{{Name: "a", Type: "http"}, {Name: "a", Type: "tcp"}}, "spec.checks[1].name"},
		"bad type":       {[]Check{{Name: "a", Type: "HTTP"}}, "spec.checks[0].type"},
		"short interval": {[]Check{{Name: "a", Type: "http", Interval: Duration(MinCheckInterval / 2), Timeout: Duration(MinCheckInterval / 4)}}, "spec.checks[0].interval"},
		"timeout >= int": {[]Check{{Name: "a", Type: "http", Interval: DefaultCheckTimeout}}, "spec.checks[0].timeout"},
		"threshold":      {[]Check{{Name: "a", Type: "http", Threshold: MaxCheckThreshold + 1}}, "spec.checks[0].threshold"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateChecks(tc.checks, "spec.checks")
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("want issue at %s, got %v", tc.path, err)
			}
		})
	}
}
