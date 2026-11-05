package resource

import "testing"

func TestUnavailableReasonsHaveCodes(t *testing.T) {
	if len(unavailableReasons) == 0 {
		t.Fatal("no reasons")
	}
	for text, m := range unavailableReasons {
		if m.Code == "" || m.Message != text {
			t.Errorf("reason %q has message %+v", text, m)
		}
		if got := ReasonMessage(text); got.Code != m.Code || got.Message != text {
			t.Errorf("ReasonMessage(%q) = %+v", text, got)
		}
	}
	got := ReasonMessage(RouterNotManaged)
	if got.Code != "action.notManaged" || got.Params["type"] != "router" {
		t.Errorf("router not managed = %+v", got)
	}
	// A caller may edit the params of what it gets without changing the table.
	got.Params["type"] = "changed"
	if ReasonMessage(RouterNotManaged).Params["type"] != "router" {
		t.Error("ReasonMessage shares its parameter map")
	}
}
