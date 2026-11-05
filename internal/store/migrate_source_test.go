package store

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// testEnv is a migration environment whose build directory is empty.
func testEnv(t *testing.T) migrateEnv {
	t.Helper()
	return migrateEnv{git: "git", workDir: filepath.Join(t.TempDir(), "work"), runner: execx.New()}
}

// sourceEnv is a migration environment over a temporary build directory.
type sourceEnv struct {
	t    *testing.T
	env  migrateEnv
	work string
}

func newSourceEnv(t *testing.T) *sourceEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	work := filepath.Join(t.TempDir(), "work")
	return &sourceEnv{t: t, work: work, env: migrateEnv{git: "git", workDir: work, runner: execx.New()}}
}

// checkout creates a repository at dir with the given remotes.
func (e *sourceEnv) checkout(dir string, remotes ...string) string {
	e.t.Helper()
	e.git("", "init", "-q", dir)
	for i := 0; i+1 < len(remotes); i += 2 {
		e.git(dir, "remote", "add", remotes[i], remotes[i+1])
	}
	return dir
}

func (e *sourceEnv) git(dir string, args ...string) {
	e.t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test fixture with constant arguments
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// legacyService is a schema 14 service document.
func legacyService(name, repo, remote string) string {
	return `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"` + name + `"},"spec":{"type":"service","adapter":"systemd",` +
		`"config":{"unit":"` + name + `.service","scope":"system","source":{"repo":"` + repo + `","remote":"` + remote + `","branch":"main","build":"make","timeout":"20m"}},` +
		`"checks":[{"name":"http","type":"http","interval":"15s","props":{"port":8080,"url":"http://127.0.0.1:8080/"}}]}}`
}

const plainService = `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"db"},"spec":{"type":"service","adapter":"docker","config":{"container":"db"}}}`

// insertResource stores a resource with its versions at schema 14.
func insertResource(t *testing.T, db *sql.DB, id, name, phase string, docs ...string) {
	t.Helper()
	execAll(t, db, `INSERT INTO resources VALUES(`+quote(id)+`,`+quote(name)+`,'service','systemd',`+quote(phase)+`,`+itoa(len(docs))+`,1,1)`)
	for i, d := range docs {
		execAll(t, db, `INSERT INTO resource_versions VALUES(`+quote(id)+`,`+itoa(i+1)+`,`+quote(d)+`,0,1,'user','u1','alice')`)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func documents(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, err := db.Query(`SELECT resource_id || '/' || version, document FROM resource_versions UNION ALL
		SELECT 'own/' || name, document || '|' || pending FROM config_ownership`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

func schemaOf(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func sourceOf(t *testing.T, doc string) *model.Source {
	t.Helper()
	r, err := model.DecodeResource([]byte(doc))
	if err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	canon, _ := model.Encode(r)
	if string(canon) != doc {
		t.Fatalf("not canonical:\n%s\n%s", doc, canon)
	}
	cfg, _ := r.Service()
	return cfg.Source
}

func latestSchema(t *testing.T) int {
	t.Helper()
	v, err := SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSourceMigration(t *testing.T) {
	if v, err := SchemaVersion(); err != nil || v < sourceMigration {
		t.Fatalf("schema version = %d %v", v, err)
	}
	e := newSourceEnv(t)
	db := legacyDB(t, 14)
	web := e.checkout(filepath.Join(e.work, "web"), "origin", "https://github.com/acme/web.git", "upstream", "git@github.com:acme/web.git")
	elsewhere := e.checkout(filepath.Join(t.TempDir(), "old-arch"), "origin", "/home/dev/arch")
	current := legacyService("web", web, "origin")
	insertResource(t, db, "w", "web", "managed",
		legacyService("web", "/gone/web", "origin"), // the path no longer exists: the current URL
		legacyService("web", web, "upstream"),       // another remote of the same checkout
		plainService,                                // a version without a source stays as it is
		current)
	// An archive is not deployed again; its versions read their own path.
	insertResource(t, db, "a", "arch", "unmanaged", legacyService("arch", elsewhere, "origin"))
	insertResource(t, db, "d", "db", "managed", plainService)
	execAll(t, db, `INSERT INTO config_ownership VALUES('Resource','web','repo','web.yaml','c1',4,`+quote(current)+`,`+
		quote(legacyService("web", "/gone/web", "origin"))+`,'web.yaml','c2',0,0,1)`)

	if err := migrate(context.Background(), db, e.env); err != nil {
		t.Fatal(err)
	}
	if v, want := schemaOf(t, db), latestSchema(t); v != want {
		t.Fatalf("schema = %d", v)
	}
	docs := documents(t, db)
	want := map[string]string{
		"w/1": "https://github.com/acme/web.git", "w/2": "git@github.com:acme/web.git", "w/4": "https://github.com/acme/web.git",
		"a/1": "/home/dev/arch",
	}
	for k, url := range want {
		src := sourceOf(t, docs[k])
		if src.URL != url || src.Repo != "" || src.Remote != "" || src.Branch != "main" || src.Build != "make" {
			t.Errorf("%s: source = %+v", k, src)
		}
	}
	if docs["w/3"] != plainService || docs["d/1"] != plainService {
		t.Errorf("documents without a source changed: %s %s", docs["w/3"], docs["d/1"])
	}
	if !strings.Contains(docs["w/4"], `"props":{"port":8080,`) {
		t.Errorf("check props changed: %s", docs["w/4"])
	}
	doc, pending, _ := strings.Cut(docs["own/web"], "|")
	if doc != docs["w/4"] || sourceOf(t, pending).URL != "https://github.com/acme/web.git" {
		t.Errorf("ownership = %s", docs["own/web"])
	}
}

func TestSourceMigrationRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *sourceEnv, db *sql.DB)
		want  string
	}{
		{"checkout elsewhere", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.t.TempDir(), "web"), "origin", "https://github.com/acme/web.git")
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", repo, "origin"))
		}, "LCM now keeps the build checkout of web at"},
		{"retired checkout elsewhere", func(e *sourceEnv, db *sql.DB) {
			insertResource(e.t, db, "w", "web", "retired", legacyService("web", "/srv/web", "origin"))
		}, "move the checkout"},
		{"missing checkout", func(e *sourceEnv, db *sql.DB) {
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", filepath.Join(e.work, "web"), "origin"))
		}, "is missing or not a clone"},
		{"remote not set", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.work, "web"), "upstream", "https://github.com/acme/web.git")
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", repo, "origin"))
		}, "it is not set"},
		{"credentials", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.work, "web"), "origin", "https://bot:s3cret@example.com/web.git")
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", repo, "origin"))
		}, "carries credentials"},
		{"inside the build directory", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.work, "web"), "origin", filepath.Join(e.work, "other"))
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", repo, "origin"))
		}, "inside LCM's build directory"},
		{"relative url", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.work, "web"), "origin", "../web.git")
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", repo, "origin"))
		}, "must be a remote URL"},
		{"gone without another version", func(e *sourceEnv, db *sql.DB) {
			insertResource(e.t, db, "a", "arch", "unmanaged", legacyService("arch", "/gone/arch", "origin"))
		}, "no longer exists and no other version of arch"},
		{"bad historical version", func(e *sourceEnv, db *sql.DB) {
			repo := e.checkout(filepath.Join(e.work, "web"), "origin", "https://github.com/acme/web.git")
			old := e.checkout(filepath.Join(e.t.TempDir(), "old"))
			insertResource(e.t, db, "w", "web", "managed", legacyService("web", old, "origin"), legacyService("web", repo, "origin"))
		}, "service web version 1: cannot read"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e := newSourceEnv(t)
			db := legacyDB(t, 14)
			insertResource(t, db, "d", "db", "managed", plainService)
			tt.setup(e, db)
			before := documents(t, db)

			err := migrate(context.Background(), db, e.env)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "stays at schema 14") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("the error shows a credential: %v", err)
			}
			if v := schemaOf(t, db); v != resourcesMigration {
				t.Fatalf("schema = %d", v)
			}
			after := documents(t, db)
			if len(after) != len(before) {
				t.Fatalf("documents %d -> %d", len(before), len(after))
			}
			for k, v := range before {
				if after[k] != v {
					t.Fatalf("%s changed:\n%s\n%s", k, v, after[k])
				}
			}
		})
	}
}

func TestMigrateEnvOptions(t *testing.T) {
	e, err := newMigrateEnv("/data/lcm.db", nil)
	if err != nil || e.git != "git" || e.workDir != "/data/work" {
		t.Fatalf("defaults = %+v %v", e, err)
	}
	e, err = newMigrateEnv("/data/lcm.db", []Option{WithGit("/usr/bin/git"), WithWorkDir("/srv/lcm/work/")})
	if err != nil || e.git != "/usr/bin/git" || e.workDir != "/srv/lcm/work" {
		t.Fatalf("options = %+v %v", e, err)
	}
	if _, err := newMigrateEnv("/data/lcm.db", []Option{WithWorkDir("work")}); err == nil {
		t.Fatal("relative work dir accepted")
	}
}

// A database still at lcm/v1 is converted by migration 14 with the source
// as it was, then by migration 15.
func TestSourceMigrationFromV1(t *testing.T) {
	e := newSourceEnv(t)
	db := legacyDB(t, 13)
	e.checkout(filepath.Join(e.work, "web"), "origin", "https://github.com/acme/web.git")
	v1 := `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"web"},"spec":{"host":"local","runtime":{"type":"external"},` +
		`"source":{"repo":"` + filepath.Join(e.work, "web") + `","remote":"origin","branch":"main"}}}`
	execAll(t, db,
		`INSERT INTO service_versions VALUES('web',1,`+quote(v1)+`,NULL,10,'user','u1','alice')`,
		`INSERT INTO services VALUES('web','local',1,'running',21,10,20)`,
		`INSERT INTO config_ownership VALUES('Service','web','repo','web.yaml','c1',1,`+quote(v1)+`,'','','',0,0,1)`,
	)
	if err := migrate(context.Background(), db, e.env); err != nil {
		t.Fatal(err)
	}
	docs := documents(t, db)
	if len(docs) != 2 {
		t.Fatalf("documents = %v", docs)
	}
	for k, d := range docs {
		d, _, _ = strings.Cut(d, "|")
		if src := sourceOf(t, d); src.URL != "https://github.com/acme/web.git" || src.Branch != "main" {
			t.Errorf("%s: source = %+v", k, src)
		}
	}
}
