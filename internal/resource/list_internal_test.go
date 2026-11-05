package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/store/storetest"
)

// A page of the resource list seeks the active name index instead of
// sorting every resource, with or without filters.
func TestListUsesTheActiveNameIndex(t *testing.T) {
	db := storetest.Open(t)
	for _, f := range []Filter{
		{IncludeRetired: true, Limit: 51},
		{IncludeRetired: true, After: "web", Limit: 51},
		{Type: "service", Phase: PhaseRetired, Labels: map[string]string{"tier": "web"}, After: "a", Limit: 101},
	} {
		where, args, err := listCondition(f)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Reader().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+selectRecords+where, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		all := strings.Join(plan, "\n")
		t.Logf("%+v:\n%s", f, all)
		if !strings.Contains(all, "USING INDEX resources_active_name") || strings.Contains(all, "TEMP B-TREE") {
			t.Errorf("filter %+v: plan does not walk the name index:\n%s", f, all)
		}
	}
}
