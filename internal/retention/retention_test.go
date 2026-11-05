package retention

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

type env struct {
	ctx context.Context
	db  *store.DB
	clk *clock.Fake
	p   *Pruner
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	p, err := New(Config{DB: db, Clock: clk, VersionKeep: 3, OperationAge: 90 * 24 * time.Hour, FailedLoginKeep: 4})
	if err != nil {
		t.Fatal(err)
	}
	return &env{ctx: context.Background(), db: db, clk: clk, p: p}
}

func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, args...); return err }); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (e *env) list(t *testing.T, query string, args ...any) string {
	t.Helper()
	rows, err := e.db.Reader().Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestNewValidates(t *testing.T) {
	db := storetest.Open(t)
	for _, cfg := range []Config{
		{},
		{DB: db, VersionKeep: 0, OperationAge: time.Hour, FailedLoginKeep: 1},
		{DB: db, VersionKeep: 1, OperationAge: 0, FailedLoginKeep: 1},
		{DB: db, VersionKeep: 1, OperationAge: time.Hour, FailedLoginKeep: 0},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}

func TestVersionsKeepNewestAndReferenced(t *testing.T) {
	e := newEnv(t)
	addVersions := func(table, name string, n int) {
		for v := 1; v <= n; v++ {
			e.exec(t, `INSERT INTO `+table+`(name,version,document,created_at,created_by_kind,created_by_id,created_by_name) VALUES(?,?,'{}',0,'user','1','a')`, name, v)
		}
	}
	own := func(kind, name string, version int) {
		e.exec(t, `INSERT INTO config_ownership(kind,name,source,path,commit_id,applied_version,document,applied_at) VALUES(?,?,'git','f.yaml','c',?,'{}',0)`, kind, name, version)
	}
	addVersions("policy_versions", "p", 10)
	e.exec(t, `INSERT INTO policy_deployments(name,version,deployed_at) VALUES('p',2,0)`)
	e.exec(t, `INSERT INTO runs(id,policy,version,status,trigger_node,event,mode,created_at) VALUES('r1','p',4,'succeeded','t','{}','live',0)`)
	own("Policy", "p", 6)
	addVersions("module_versions", "m", 6)
	own("Module", "m", 1)
	for _, r := range []struct {
		id, name, phase string
		versions        int
	}{{"id-r", "r", "managed", 6}, {"id-old", "old", "unmanaged", 5}} {
		e.exec(t, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES(?,?,'service','command',?,?,0,0)`, r.id, r.name, r.phase, r.versions)
		for v := 1; v <= r.versions; v++ {
			e.exec(t, `INSERT INTO resource_versions(resource_id,version,document,created_at,actor_kind,actor_id,actor_name) VALUES(?,?,'{}',0,'user','1','a')`, r.id, v)
		}
	}
	own("Resource", "r", 2)
	// A short history is untouched.
	addVersions("policy_versions", "short", 2)

	n, err := e.p.Versions(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("deleted %d, want 10", n)
	}
	for query, want := range map[string]string{
		`SELECT version FROM policy_versions WHERE name='p' ORDER BY version`:               "2,4,6,8,9,10",
		`SELECT version FROM policy_versions WHERE name='short' ORDER BY version`:           "1,2",
		`SELECT version FROM module_versions WHERE name='m' ORDER BY version`:               "1,4,5,6",
		`SELECT version FROM resource_versions WHERE resource_id='id-r' ORDER BY version`:   "2,4,5,6",
		`SELECT version FROM resource_versions WHERE resource_id='id-old' ORDER BY version`: "3,4,5",
	} {
		if got := e.list(t, query); got != want {
			t.Errorf("%s = %s, want %s", query, got, want)
		}
	}
	// The current resource version is kept even when it is not among the
	// newest stored ones.
	e.exec(t, `UPDATE resources SET version=3 WHERE id='id-old'`)
	e.exec(t, `INSERT INTO resource_versions(resource_id,version,document,created_at,actor_kind,actor_id,actor_name) VALUES('id-old',6,'{}',0,'user','1','a'),('id-old',7,'{}',0,'user','1','a')`)
	if _, err = e.p.Versions(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.list(t, `SELECT version FROM resource_versions WHERE resource_id='id-old' ORDER BY version`); got != "3,5,6,7" {
		t.Errorf("archive versions = %s", got)
	}
}

func TestOperationsKeepUnfinished(t *testing.T) {
	e := newEnv(t)
	old := e.clk.Now().UnixMilli()
	e.clk.Advance(100 * 24 * time.Hour)
	recent := e.clk.Now().Add(-time.Hour).UnixMilli()

	e.exec(t, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES('m','m','machine','local','managed',1,0,0),('g','g','router','xiaomi','managed',1,0,0)`)
	for _, op := range []struct {
		id, resource string
		unresolved   int
		at           int64
	}{{"done-old", "m", 0, old}, {"done-recent", "m", 0, recent}, {"open-old", "g", 1, old}} {
		e.exec(t, `INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,unresolved,created_at,updated_at,body) VALUES(?,?,?,'h','reboot','x',?,?,?,'{}')`,
			op.id, op.resource, op.id, op.unresolved, op.at, op.at)
	}
	for _, op := range []struct {
		id, state string
		at        int64
	}{
		{"succeeded-old", "succeeded", old}, {"failed-old", "failed", old}, {"prepared-old", "prepared", old},
		{"running-old", "running", old}, {"indeterminate-old", "indeterminate", old}, {"awaiting-old", "awaiting-local-approval", old},
		{"succeeded-recent", "succeeded", recent},
	} {
		e.exec(t, `INSERT INTO security_host_operations(id,action,actor,request_digest,request_body,preview_body,operation_body,created_at,updated_at) VALUES(?,'aide.check','user:1','d','{}','{}',json_object('state',?),?,?)`,
			op.id, op.state, op.at, op.at)
	}
	for _, inc := range []struct {
		id, state string
		at        int64
		test      bool
		queued    bool
	}{
		{"test-done-old", "resolved", old, true, false},
		{"test-retired-old", "retired", old, true, false},
		{"test-queued-old", "resolved", old, true, true},
		{"test-firing-old", "firing", old, true, false},
		{"test-done-recent", "resolved", recent, true, false},
		{"real-done-old", "resolved", old, false, false},
	} {
		e.exec(t, `INSERT INTO security_incidents(id,condition_key,machine_id,service,source,fingerprint,state,severity,summary,opened_at,observed_at,updated_at,occurrences,revision) VALUES(?,?,'m','','collector','f',?,'info','s',?,?,?,1,1)`,
			inc.id, inc.id, inc.state, inc.at, inc.at, inc.at)
		if inc.test {
			e.exec(t, `INSERT INTO security_notification_tests(request_id,incident_id,reason,actor,created_at) VALUES(?,?,'r','user:1',?)`, "req-"+inc.id, inc.id, inc.at)
		}
		if inc.queued {
			e.exec(t, `INSERT INTO security_outbox(incident_id,revision,created_at,next_at) VALUES(?,2,?,?)`, inc.id, inc.at, inc.at)
		}
	}

	n, err := e.p.Operations(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1+3+2 {
		t.Errorf("deleted %d, want 6", n)
	}
	for query, want := range map[string]string{
		`SELECT id FROM resource_operations ORDER BY id`:                 "done-recent,open-old",
		`SELECT id FROM security_host_operations ORDER BY id`:            "awaiting-old,indeterminate-old,running-old,succeeded-recent",
		`SELECT id FROM security_incidents ORDER BY id`:                  "real-done-old,test-done-recent,test-firing-old,test-queued-old",
		`SELECT incident_id FROM security_notification_tests ORDER BY 1`: "test-done-recent,test-firing-old,test-queued-old",
	} {
		if got := e.list(t, query); got != want {
			t.Errorf("%s = %s, want %s", query, got, want)
		}
	}
}

func TestFailedLoginsAreCapped(t *testing.T) {
	e := newEnv(t)
	record := func(action, outcome, target string) {
		e.exec(t, `INSERT INTO audit_log(time,actor_kind,actor_id,actor_name,action,target,outcome) VALUES(0,'user','','x',?,?,?)`, action, target, outcome)
	}
	for i := range 7 {
		record("auth.login", "denied", string(rune('a'+i)))
		record("auth.login", "ok", string(rune('a'+i)))
	}
	record("resource.create", "denied", "kept")
	if n, err := e.p.FailedLogins(e.ctx); err != nil || n != 3 {
		t.Fatalf("deleted %d %v, want 3", n, err)
	}
	if got := e.list(t, `SELECT target FROM audit_log WHERE action='auth.login' AND outcome='denied' ORDER BY id`); got != "d,e,f,g" {
		t.Errorf("failed logins = %s", got)
	}
	if got := e.list(t, `SELECT COUNT(*) FROM audit_log WHERE outcome<>'denied' OR action<>'auth.login'`); got != "8" {
		t.Errorf("other entries = %s, want 8", got)
	}
	if n, err := e.p.FailedLogins(e.ctx); err != nil || n != 0 {
		t.Fatalf("second run deleted %d %v", n, err)
	}
}

// TestRulesUseIndexes shows that each rule finds its candidates through an
// index rather than by scanning a table.
func TestRulesUseIndexes(t *testing.T) {
	db := storetest.Open(t)
	for _, tc := range []struct {
		r    rule
		want []string
	}{
		{failedLogins, []string{"INDEX audit_log_action (action=? AND outcome=?"}},
		{operationRules[0], []string{"INDEX resource_operations_finished"}},
		{operationRules[1], []string{"INDEX security_host_operations_time"}},
		{operationRules[2], []string{"INDEX security_notification_tests_created"}},
		{versionRules[1], []string{"INDEX runs_policy_version", "PRIMARY KEY"}},
	} {
		plan := strings.Join(storetest.QueryPlan(t, db, tc.r.query, 1, batch), "\n")
		for _, want := range tc.want {
			if !strings.Contains(plan, want) {
				t.Errorf("%s: plan lacks %q:\n%s", tc.r.name, want, plan)
			}
		}
	}
}
