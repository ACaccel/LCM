package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

// legacyDB returns a database migrated up to and including version upTo,
// as an older binary left it.
func legacyDB(t *testing.T, upTo int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL); PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= upTo {
			if err := apply(context.Background(), db, m, testEnv(t)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func execAll(t *testing.T, db *sql.DB, queries ...string) {
	t.Helper()
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func indent(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

const (
	webV1  = `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"web"},"spec":{"host":"local","runtime":{"type":"systemd","unit":"web.service","scope":"system"},"dependsOn":["db"],"checks":[{"name":"http","type":"http","props":{"url":"http://127.0.0.1:8080/"}}]}}`
	webV1a = `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"web"},"spec":{"host":"local","runtime":{"type":"external"}}}`
	dbV1   = `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"db"},"spec":{"host":"local","runtime":{"type":"docker","container":"db"}}}`
	oldV1  = `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"old"},"spec":{"host":"local","runtime":{"type":"command","stop":"old-stop"}}}`
	goneV1 = `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"gone"},"spec":{"host":"local","runtime":{"type":"compose","project":"gone","file":"/srv/gone/compose.yaml"}}}`
	wsV1   = `{"apiVersion":"lcm/v1","kind":"Machine","metadata":{"name":"workstation"},"spec":{"host":"local","monitoring":{"interval":"30s","staleAfter":"1m30s","filesystems":["/"],"journal":false,"thresholds":{"cpuPercent":90,"memoryPercent":90,"diskPercent":90,"consecutive":3,"hysteresis":5}}}}`
	modV1  = `{"apiVersion":"lcm/v1","kind":"Module","metadata":{"name":"stopper"},"spec":{"params":[{"name":"target"}],"nodes":[{"id":"in","type":"module.input","next":{"out":["act"]}},{"id":"act","type":"lifecycle.stop","props":{"service":"{{ params.target }}"},"next":{"out":["out"]}},{"id":"out","type":"module.output"}]}}`
	polV1  = `{"apiVersion":"lcm/v1","kind":"Policy","metadata":{"name":"restart"},"spec":{"nodes":[` +
		`{"id":"start","type":"trigger.signal","props":{"kinds":["health.changed"],"services":["web"]},"next":{"out":["check"]}},` +
		`{"id":"check","type":"flow.if","props":{"condition":"service[trigger.service].health == 'down'"},"next":{"true":["restart"]}},` +
		`{"id":"restart","type":"lifecycle.restart","props":{"service":"{{ trigger.service }}"},"next":{"out":["stop"]}},` +
		`{"id":"stop","type":"module","props":{"module":"stopper","params":{"target":"{{ keys(service)[0] }}"}}}]}}`
)

func TestResourceMigration(t *testing.T) {
	ctx := context.Background()
	db := legacyDB(t, 13)
	execAll(t, db,
		// web: active with two versions; db: active; old: retired; gone: deleted.
		`INSERT INTO service_versions VALUES('web',1,`+quote(indent(t, webV1a))+`,NULL,10,'user','u1','alice')`,
		`INSERT INTO service_versions VALUES('web',2,`+quote(indent(t, webV1))+`,NULL,20,'token','t1','ci')`,
		`INSERT INTO services VALUES('web','local',2,'running',21,10,20)`,
		`INSERT INTO service_versions VALUES('db',1,`+quote(indent(t, dbV1))+`,NULL,5,'system','cli','cli')`,
		`INSERT INTO services VALUES('db','local',1,'stopped',6,5,5)`,
		`INSERT INTO service_versions VALUES('old',1,`+quote(indent(t, oldV1))+`,NULL,7,'system','cli','cli')`,
		`INSERT INTO services VALUES('old','local',1,'retired',8,7,7)`,
		`INSERT INTO service_versions VALUES('gone',1,`+quote(indent(t, goneV1))+`,NULL,3,'system','cli','cli')`,
		`INSERT INTO service_versions VALUES('gone',2,`+quote(indent(t, goneV1))+`,1,4,'system','cli','cli')`,
		`INSERT INTO timeline(service,time,kind,summary) VALUES('web',20,'config','saved')`,
		`INSERT INTO deployments(service,action,started_at,outcome,actor_kind,actor_id,actor_name) VALUES('web','deploy',20,'succeeded','user','u1','alice')`,
		`INSERT INTO action_completions VALUES('web','restart','k',20,'{}')`,
		`INSERT INTO monitor_checks(check_id,service,name,type,critical,interval_ms,status,since_ms) VALUES('web/http','web','http','http',1,30000,'up',1)`,
		`INSERT INTO monitor_services VALUES('web','healthy',1,'{}',1)`,
		`INSERT INTO monitor_alerts(source,fingerprint,service,name,severity,started_at,updated_at) VALUES('am','f','web','HighLatency','warning',1,1)`,
		`INSERT INTO locks VALUES('service:web','owner',1,99)`,
		`INSERT INTO locks VALUES('custom','owner',1,99)`,

		// The local machine with observations, an operation and maintenance.
		`INSERT INTO machines(id,name,version,phase,identity_hash,status,created_at,updated_at) VALUES('m-1','workstation',1,'managed','hash','{"health":"healthy"}',1,2)`,
		`INSERT INTO machine_versions VALUES('m-1',1,`+quote(wsV1)+`,0,1,'{"kind":"user","id":"u1","name":"alice"}')`,
		`INSERT INTO machine_snapshots(machine_id,observed_at,body) VALUES('m-1',1,'{"cpuPercent":12.345678901234567}')`,
		`INSERT INTO machine_events(machine_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES('m-1','control','k1',1,1,'machine.save','{"id":0,"machineId":"m-1","kind":"machine.save"}')`,
		`INSERT INTO machine_events(machine_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES('m-1','control','k2',1,1,'machine.save','{}')`,
		`DELETE FROM machine_events WHERE source_key='k2'`,
		`INSERT INTO machine_rollups(machine_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES('m-1',0,'b',1,1.5,1,'{}')`,
		`INSERT INTO machine_cursors VALUES('m-1','journal','c',1)`,
		`INSERT INTO machine_operations VALUES('op-1','m-1','idem','hash','reboot','pending',1,1,1,'{"id":"op-1","machineId":"m-1","evidence":{"snapshot":{"cpuPercent":0.30000000000000004}}}')`,
		`INSERT INTO machine_maintenance VALUES('local','m-1','active',1,'{"machineId":"m-1","state":"active"}')`,

		// Policies, modules, a suspended run and Git ownership.
		`INSERT INTO module_versions VALUES('stopper',1,`+quote(indent(t, modV1))+`,1,'system','cli','cli')`,
		`INSERT INTO policy_versions VALUES('restart',1,`+quote(polV1)+`,`+quote(`{"stopper":`+modV1+`}`)+`,1,'system','cli','cli')`,
		`INSERT INTO policy_deployments VALUES('restart',1,0,1)`,
		`INSERT INTO runs(id,policy,version,status,trigger_node,event,mode,created_at) VALUES('run','restart',1,'suspended','start','{"id":"s1","kind":"health.changed","service":"web","source":"monitor"}','live',1)`,
		`INSERT INTO runs(id,policy,version,status,trigger_node,event,mode,created_at) VALUES('done','restart',1,'succeeded','start','{"id":"s0","kind":"health.changed","service":"web","source":"monitor"}','live',1)`,
		`INSERT INTO run_nodes(run_id,node_id,status,suspend_key) VALUES('run','wait','suspended','resume-key')`,
		`INSERT INTO run_nodes(run_id,node_id,status,props,output) VALUES('run','restart','completed','{"service":"web"}','{"action":"restart","service":"web","to":"running"}')`,
		`INSERT INTO run_nodes(run_id,node_id,status,props,output) VALUES('run','stop/act','completed','{"service":"web"}','{"action":"stop","service":"web"}')`,
		`INSERT INTO run_nodes(run_id,node_id,status,props,output) VALUES('run','check','completed','{"condition":true}','{"service":"kept"}')`,
		`INSERT INTO run_nodes(run_id,node_id,status,props,output) VALUES('done','restart','completed','{"service":"web"}','{"action":"restart","service":"web"}')`,
		`INSERT INTO config_ownership VALUES('Service','web','repo','web.yaml','c1',2,`+quote(webV1)+`,`+quote(webV1a)+`,'web2.yaml','c2',1,0,1)`,
		`INSERT INTO config_ownership VALUES('Machine','workstation','repo','ws.yaml','c1',1,`+quote(wsV1)+`,'','','',0,0,1)`,
		`INSERT INTO config_ownership VALUES('Policy','restart','repo','p.yaml','c1',1,`+quote(polV1)+`,'','','',0,1,1)`,
	)
	if err := migrate(ctx, db, testEnv(t)); err != nil {
		t.Fatal(err)
	}

	type res struct{ id, typ, adapter, phase string }
	got := map[string]res{}
	rows, err := db.Query(`SELECT id, name, type, adapter, phase FROM resources`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r res
		var name string
		if err := rows.Scan(&r.id, &name, &r.typ, &r.adapter, &r.phase); err != nil {
			t.Fatal(err)
		}
		got[name] = r
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]string{
		"web": {"service", "systemd", "managed"}, "db": {"service", "docker", "managed"},
		"old": {"service", "command", "retired"}, "gone": {"service", "compose", "unmanaged"},
		"workstation": {"machine", "local", "managed"},
	}
	if len(got) != len(want) {
		t.Fatalf("resources = %v", got)
	}
	for name, w := range want {
		if r := got[name]; r.typ != w[0] || r.adapter != w[1] || r.phase != w[2] || len(r.id) != 36 && name != "workstation" {
			t.Errorf("%s = %+v, want %v", name, r, w)
		}
	}
	if got["workstation"].id != "m-1" {
		t.Fatalf("machine ID changed: %s", got["workstation"].id)
	}

	one := func(query string, args ...any) string {
		t.Helper()
		var s string
		if err := db.QueryRow(query, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s
	}
	for name, state := range map[string]string{"web": "running", "db": "stopped", "old": "stopped"} {
		if s := one(`SELECT state FROM service_states WHERE resource_id = ?`, got[name].id); s != state {
			t.Errorf("%s state = %s", name, s)
		}
	}
	if n := one(`SELECT COUNT(*) FROM service_states WHERE resource_id = ?`, got["gone"].id); n != "0" {
		t.Fatal("unmanaged service has a state")
	}

	// Stored documents equal the converters' canonical output and decode.
	webV2, _, err := model.ConvertServiceV1JSON([]byte(webV1))
	if err != nil {
		t.Fatal(err)
	}
	if doc := one(`SELECT document FROM resource_versions WHERE resource_id = ? AND version = 2`, got["web"].id); doc != string(webV2) {
		t.Fatalf("web v2 = %s\nwant %s", doc, webV2)
	}
	r, err := model.DecodeResource(webV2)
	if err != nil || r.Spec.Relations[0].Target != "db" {
		t.Fatalf("decode web: %v %+v", err, r)
	}
	if s := one(`SELECT actor_kind || '/' || actor_id || '/' || actor_name || '/' || restored_from FROM resource_versions WHERE resource_id = ? AND version = 1`, got["web"].id); s != "user/u1/alice/0" {
		t.Fatalf("web v1 actor = %s", s)
	}
	if s := one(`SELECT restored_from || '/' || COUNT(*) FROM resource_versions WHERE resource_id = ? AND version = 2`, got["gone"].id); s != "1/1" {
		t.Fatalf("archived versions = %s", s)
	}
	wsV2, _, err := model.ConvertMachineV1JSON([]byte(wsV1))
	if err != nil {
		t.Fatal(err)
	}
	if s := one(`SELECT document || '|' || actor_kind || '/' || actor_name FROM resource_versions WHERE resource_id = 'm-1'`); s != string(wsV2)+"|user/alice" {
		t.Fatalf("machine version = %s", s)
	}
	if s := one(`SELECT identity_hash || status FROM resource_status WHERE resource_id = 'm-1'`); s != `hash{"health":"healthy"}` {
		t.Fatalf("status = %s", s)
	}

	// Observations follow the ID; bodies name it as resourceId.
	if s := one(`SELECT body FROM resource_events WHERE resource_id = 'm-1'`); !strings.Contains(s, `"resourceId":"m-1"`) || strings.Contains(s, "machineId") {
		t.Fatalf("event body = %s", s)
	}
	if s := one(`SELECT body FROM resource_operations WHERE resource_id = 'm-1'`); !strings.Contains(s, `"resourceId":"m-1"`) || !strings.Contains(s, `0.30000000000000004`) || strings.Contains(s, "machineId") {
		t.Fatalf("operation body = %s", s)
	}
	if s := one(`SELECT body FROM machine_maintenance WHERE resource_id = 'm-1'`); s != `{"state":"active","resourceId":"m-1"}` {
		t.Fatalf("maintenance body = %s", s)
	}
	if s := one(`SELECT COUNT(*) FROM resource_snapshots s JOIN resource_points p USING (resource_id) JOIN machine_cursors c USING (resource_id) WHERE resource_id = 'm-1'`); s != "1" {
		t.Fatalf("observations = %s", s)
	}
	if s := one(`SELECT seq FROM sqlite_sequence WHERE name = 'resource_events'`); s != "2" {
		t.Fatalf("event sequence = %s", s)
	}

	// Name-keyed history follows the name.
	for _, q := range []string{
		`SELECT resource FROM timeline`, `SELECT resource FROM deployments`, `SELECT resource FROM action_completions`,
		`SELECT resource FROM monitor_checks`, `SELECT resource FROM monitor_resources`, `SELECT resource FROM monitor_alerts`,
	} {
		if s := one(q); s != "web" {
			t.Errorf("%s = %s", q, s)
		}
	}
	if s := one(`SELECT group_concat(name, ',') FROM (SELECT name FROM locks ORDER BY name)`); s != "custom,resource:web" {
		t.Fatalf("locks = %s", s)
	}

	// Policies, modules and the deployed plan's module snapshot are v2.
	pol := one(`SELECT document FROM policy_versions WHERE name = 'restart'`)
	polV2, warns, err := model.ConvertPolicyV1JSON([]byte(polV1))
	if err != nil || pol != string(polV2) {
		t.Fatalf("policy = %s (%v)", pol, err)
	}
	if !strings.Contains(pol, `"resource":"{{ trigger.resource }}"`) || !strings.Contains(pol, `"resources":["web"]`) || !strings.Contains(pol, `resource[trigger.resource]`) {
		t.Fatalf("policy not rewritten: %s", pol)
	}
	if mods := one(`SELECT modules FROM policy_versions WHERE name = 'restart'`); !strings.Contains(mods, `"apiVersion":"lcm/v2"`) || !strings.Contains(mods, `"resource":"{{ params.target }}"`) {
		t.Fatalf("modules = %s", mods)
	}
	if mod := one(`SELECT document FROM module_versions WHERE name = 'stopper'`); !strings.HasPrefix(mod, "{\n  \"apiVersion\": \"lcm/v2\"") {
		t.Fatalf("module layout = %s", mod)
	}
	// The policy's own warnings (an unconvertible expression and the added
	// acknowledgeImpact), its module snapshot's and the module's.
	_, modWarns, err := model.ConvertModuleV1JSON([]byte(modV1))
	if err != nil || len(warns) != 2 || len(modWarns) != 1 || !strings.Contains(warns[1], "added acknowledgeImpact: true to lifecycle nodes restart") ||
		!strings.Contains(pol, `"acknowledgeImpact":true`) {
		t.Fatalf("warnings = %q %q (%v)", warns, modWarns, err)
	}
	stored := one(`SELECT group_concat(kind || ' ' || name || ' ' || version || ': ' || message, '|') FROM (SELECT * FROM migration_warnings ORDER BY id)`)
	if want := "Policy restart 1: " + warns[0] + "|Policy restart 1: " + warns[1] + "|Policy restart 1: " + modWarns[0] + "|Module stopper 1: " + modWarns[0]; stored != want {
		t.Fatalf("stored warnings = %s\nwant %s", stored, want)
	}
	if ev := one(`SELECT event FROM runs WHERE id = 'run'`); !strings.Contains(ev, `"resource":"web"`) || strings.Contains(ev, `"service"`) {
		t.Fatalf("active run event = %s", ev)
	}
	if ev := one(`SELECT event FROM runs WHERE id = 'done'`); !strings.Contains(ev, `"service":"web"`) {
		t.Fatalf("finished run event changed: %s", ev)
	}
	if key := one(`SELECT suspend_key FROM run_nodes WHERE run_id = 'run' AND node_id = 'wait'`); key != "resume-key" {
		t.Fatalf("checkpoint = %s", key)
	}
	// Lifecycle node checkpoints of active runs, also inside modules,
	// follow the renamed output and prop; others stay as they were.
	for _, c := range []struct{ run, node, want string }{
		{"run", "restart", `{"resource":"web"}|{"action":"restart","resource":"web","to":"running"}`},
		{"run", "stop/act", `{"resource":"web"}|{"action":"stop","resource":"web"}`},
		{"run", "check", `{"condition":true}|{"service":"kept"}`},
		{"done", "restart", `{"service":"web"}|{"action":"restart","service":"web"}`},
	} {
		if s := one(`SELECT props || '|' || output FROM run_nodes WHERE run_id = ? AND node_id = ?`, c.run, c.node); s != c.want {
			t.Errorf("%s/%s checkpoint = %s, want %s", c.run, c.node, s, c.want)
		}
	}

	// Ownership merges into Resource with converted documents.
	webA, _, _ := model.ConvertServiceV1JSON([]byte(webV1a))
	if s := one(`SELECT document || '|' || pending || '|' || pending_path || conflict_commit || acknowledged FROM config_ownership WHERE kind = 'Resource' AND name = 'web'`); s != string(webV2)+"|"+string(webA)+"|web2.yamlc21" {
		t.Fatalf("web ownership = %s", s)
	}
	if s := one(`SELECT document FROM config_ownership WHERE kind = 'Resource' AND name = 'workstation'`); s != string(wsV2) {
		t.Fatalf("machine ownership = %s", s)
	}
	if s := one(`SELECT document FROM config_ownership WHERE kind = 'Policy'`); s != string(polV2) {
		t.Fatalf("policy ownership = %s", s)
	}

	for _, table := range []string{"services", "service_versions", "hosts", "machines", "machine_versions", "machine_events", "monitor_services", "config_ownership_v1"} {
		if n := one(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, table); n != "0" {
			t.Errorf("table %s still exists", table)
		}
	}
	if s := one(`PRAGMA integrity_check`); s != "ok" {
		t.Fatalf("integrity %s", s)
	}
	fk, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		t.Fatal("foreign key violation")
	}
	if err = fk.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceMigrationRefusesNameClash(t *testing.T) {
	db := legacyDB(t, 13)
	clash := strings.ReplaceAll(dbV1, `"db"`, `"workstation"`)
	execAll(t, db,
		`INSERT INTO service_versions VALUES('workstation',1,`+quote(clash)+`,NULL,5,'system','cli','cli')`,
		`INSERT INTO services VALUES('workstation','local',1,'stopped',6,5,5)`,
		`INSERT INTO machines(id,name,version,created_at,updated_at) VALUES('m-1','workstation',1,1,2)`,
		`INSERT INTO machine_versions VALUES('m-1',1,`+quote(wsV1)+`,0,1,'{"kind":"user","id":"u1","name":"alice"}')`,
	)
	err := migrate(context.Background(), db, testEnv(t))
	if err == nil || !strings.Contains(err.Error(), `both named "workstation"`) {
		t.Fatalf("err = %v", err)
	}
	// The failed migration left the previous schema intact.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM services`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("services after failure: %d %v", n, err)
	}
}

func TestRefuseFutureStorageSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	version, err := SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO schema_migrations VALUES(?,'future',1)`, version+1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path)
	if err == nil {
		_ = other.Close()
		t.Fatal("future schema accepted")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatal(err)
	}
}
