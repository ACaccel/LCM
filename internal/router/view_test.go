package router

import (
	"encoding/json"
	"testing"
)

func TestSnapshotViewMessages(t *testing.T) {
	v := NewSnapshotView(Snapshot{
		Outcome: OutcomeUnreachable, Reason: "router unreachable",
		Sources: map[string]Coverage{
			"status": {State: "unavailable", Reason: "code 3"},
			"wan":    {State: "unavailable", Reason: "transport error"},
			"extra":  {State: "unavailable", Reason: "something new"},
			"ok":     {State: "available"},
		},
	})
	if v.ReasonMessage == nil || v.ReasonMessage.Code != "health.unreachable" {
		t.Errorf("reason message = %+v", v.ReasonMessage)
	}
	want := map[string]struct{ code, param string }{
		"status": {"router.code", "3"},
		"wan":    {"router.transportError", ""},
		"extra":  {"", ""},
	}
	for name, w := range want {
		m := v.Sources[name].ReasonMessage
		if m == nil || m.Code != w.code || (w.param != "" && m.Params["code"] != w.param) {
			t.Errorf("source %s message = %+v, want %+v", name, m, w)
		}
	}
	if v.Sources["ok"].ReasonMessage != nil {
		t.Errorf("a source without a reason has a message")
	}

	// The view replaces the stored fields on the wire rather than adding duplicates.
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["reason"] != "router unreachable" || wire["reasonMessage"] == nil || wire["sources"].(map[string]any)["wan"].(map[string]any)["reasonMessage"] == nil {
		t.Errorf("wire form = %s", b)
	}

	if none := NewSnapshotView(Snapshot{Outcome: OutcomeOK}); none.ReasonMessage != nil || none.Sources == nil {
		t.Errorf("empty view = %+v", none)
	}
}

func TestCoverageMessageWithStatus(t *testing.T) {
	m := coverageMessage("http 502")
	if m.Code != "router.http" || m.Params["status"] != "502" || m.Message != "http 502" {
		t.Errorf("http message = %+v", m)
	}
}
