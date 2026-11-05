package audit

import (
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/store/storetest"
)

// TestSearchUsesFilterIndexes shows that every filter of the audit API
// reads its own index instead of scanning the log.
func TestSearchUsesFilterIndexes(t *testing.T) {
	db := storetest.Open(t)
	for _, tc := range []struct {
		q     Query
		index string
	}{
		{Query{Actor: "admin"}, "audit_log_actor"},
		{Query{Action: "service"}, "audit_log_action"},
		{Query{Target: "web", Before: 10}, "audit_log_target"},
	} {
		query, args := tc.q.sql()
		plan := strings.Join(storetest.QueryPlan(t, db, query, args...), "\n")
		if !strings.Contains(plan, "INDEX "+tc.index) {
			t.Errorf("%+v: plan does not use %s:\n%s", tc.q, tc.index, plan)
		}
	}
}
