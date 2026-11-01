package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineMigrationPreservesLegacyData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL); PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version <= 8 {
			if err := apply(ctx, db, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := []string{
		`INSERT INTO service_versions VALUES('legacy',1,'{}',NULL,1,'cli','cli','cli')`,
		`INSERT INTO services VALUES('legacy','local',1,'running',1,1,1)`,
		`INSERT INTO policy_versions VALUES('policy',1,'{}','{}',1,'cli','cli','cli')`,
		`INSERT INTO policy_deployments VALUES('policy',1,0,1)`,
		`INSERT INTO runs(id,policy,version,status,trigger_node,event,mode,created_at) VALUES('run','policy',1,'suspended','start','{}','live',1)`,
		`INSERT INTO run_nodes(run_id,node_id,status,suspend_key) VALUES('run','wait','suspended','resume-key')`,
		`INSERT INTO config_ownership(kind,name,source,path,commit_id,applied_version,document,pending,pending_path,conflict_commit,acknowledged,orphaned,applied_at) VALUES('Service','legacy','repo','legacy.yaml','commit-a',1,'{}','{"pending":true}','new.yaml','commit-b',1,1,1)`,
	}
	for _, q := range queries {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var state, key, pending, integrity string
	if err = db.QueryRow(`SELECT state FROM services WHERE name='legacy'`).Scan(&state); err != nil || state != "running" {
		t.Fatalf("service %s %v", state, err)
	}
	if err = db.QueryRow(`SELECT suspend_key FROM run_nodes WHERE run_id='run'`).Scan(&key); err != nil || key != "resume-key" {
		t.Fatalf("checkpoint %s %v", key, err)
	}
	if err = db.QueryRow(`SELECT pending FROM config_ownership WHERE kind='Service' AND name='legacy' AND acknowledged=1 AND orphaned=1 AND conflict_commit='commit-b'`).Scan(&pending); err != nil || pending != `{"pending":true}` {
		t.Fatalf("ownership %s %v", pending, err)
	}
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %s %v", integrity, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
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
