package catalog_test

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
)

func TestEntryMessage(t *testing.T) {
	sha := "0123456789abcdef0123"
	cases := []struct {
		name   string
		entry  catalog.Entry
		code   string
		params map[string]string
	}{
		{"saved", catalog.Entry{Kind: catalog.KindConfig, Data: map[string]any{"version": 3, "restoredFrom": 0, "actor": "admin"}},
			"timeline.configSaved", map[string]string{"version": "3", "actor": "admin"}},
		{"restored", catalog.Entry{Kind: catalog.KindConfig, Data: map[string]any{"version": 4, "restoredFrom": 2, "actor": "admin"}},
			"timeline.configRestored", map[string]string{"version": "4", "from": "2", "actor": "admin"}},
		{"phase", catalog.Entry{Kind: catalog.KindConfig, Data: map[string]any{"phase": "retired", "previous": "managed", "actor": "admin", "reason": "old"}},
			"timeline.phase", map[string]string{"phase": "retired", "previous": "managed", "actor": "admin", "reason": "old"}},
		{"lifecycle", catalog.Entry{Kind: catalog.KindLifecycle, Data: map[string]any{"state": "running", "previous": "stopped", "action": "start"}},
			"timeline.lifecycle", map[string]string{"state": "running", "previous": "stopped", "action": "start"}},
		{"deployment", catalog.Entry{Kind: catalog.KindDeployment, Data: map[string]any{"action": "deploy", "outcome": "succeeded", "version": sha}},
			"timeline.action", map[string]string{"action": "deploy", "outcome": "succeeded", "job": "", "version": sha[:12], "instance": ""}},
		{"job", catalog.Entry{Kind: catalog.KindJob, Data: map[string]any{"action": "run-job", "outcome": "failed", "job": "reindex"}},
			"timeline.action", map[string]string{"action": "run-job", "outcome": "failed", "job": "reindex", "version": "", "instance": ""}},
		{"check", catalog.Entry{Kind: "check.changed", Data: map[string]any{"data": map[string]any{"check": "http", "previous": "up", "status": "down", "message": "HTTP 500"}}},
			"timeline.check", map[string]string{"check": "http", "previous": "up", "status": "down", "message": "HTTP 500"}},
		{"health", catalog.Entry{Kind: "health.changed", Data: map[string]any{"data": map[string]any{"previous": "healthy", "health": "down"}}},
			"timeline.health", map[string]string{"previous": "healthy", "health": "down"}},
		{"alert", catalog.Entry{Kind: "alert", Data: map[string]any{"data": map[string]any{"alert": "DiskFull", "status": "firing"}}},
			"timeline.alert", map[string]string{"alert": "DiskFull", "status": "firing"}},
		{"conflict", catalog.Entry{Kind: catalog.KindConfigConflict, Data: map[string]any{"source": "infra", "commit": sha, "path": "a.yaml", "adopt": false}},
			"timeline.configConflict", map[string]string{"source": "infra", "commit": sha[:12], "path": "a.yaml"}},
		{"adopt", catalog.Entry{Kind: catalog.KindConfigConflict, Data: map[string]any{"source": "infra", "commit": sha, "path": "a.yaml", "adopt": true}},
			"timeline.configAdopt", map[string]string{"source": "infra", "commit": sha[:12], "path": "a.yaml"}},
		{"resolved", catalog.Entry{Kind: catalog.KindConfigConflict, Data: map[string]any{"source": "infra", "resolved": true, "reason": catalog.ConflictReleased}},
			"timeline.configResolved", map[string]string{"source": "infra", "reason": catalog.ConflictReleased}},
		{"invalid", catalog.Entry{Kind: catalog.KindConfigInvalid, Data: map[string]any{"source": "infra", "commit": sha, "path": "a.yaml", "issues": "spec: required"}},
			"timeline.configInvalid", map[string]string{"source": "infra", "commit": sha[:12], "path": "a.yaml", "issues": "spec: required"}},
		// Entries written before the data carried what a code needs keep
		// their English summary only.
		{"legacy conflict", catalog.Entry{Kind: catalog.KindConfigConflict, Data: map[string]any{"source": "infra", "commit": sha, "path": "a.yaml"}}, "", map[string]string{}},
		{"legacy resolved", catalog.Entry{Kind: catalog.KindConfigConflict, Data: map[string]any{"source": "infra", "resolved": true}}, "", map[string]string{}},
		{"legacy invalid", catalog.Entry{Kind: catalog.KindConfigInvalid, Data: map[string]any{"source": "infra"}}, "", map[string]string{}},
		{"unknown kind", catalog.Entry{Kind: "something.new", Data: map[string]any{}}, "", map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.entry.Summary = "english summary"
			// Stored entries come back from JSON, with numbers as float64.
			for _, e := range []catalog.Entry{c.entry, roundTrip(t, c.entry)} {
				m := catalog.EntryMessage(e)
				if m.Code != c.code || !maps.Equal(m.Params, c.params) || m.Message != "english summary" {
					t.Fatalf("message = %+v, want code %q params %v", m, c.code, c.params)
				}
			}
		})
	}
}

func roundTrip(t *testing.T, e catalog.Entry) catalog.Entry {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var out catalog.Entry
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
