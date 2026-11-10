package selfbackup_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/selfbackup"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

type recorder struct {
	rows   []catalog.BackupRecord
	pruned []string
	last   time.Time
}

func (r *recorder) AddBackup(_ context.Context, rec catalog.BackupRecord) (int64, error) {
	r.rows = append(r.rows, rec)
	if rec.Outcome == catalog.OutcomeSucceeded {
		r.last = rec.StartedAt
	}
	return int64(len(r.rows)), nil
}

func (r *recorder) MarkBackupsPruned(_ context.Context, _ string, ids []string) error {
	r.pruned = append(r.pruned, ids...)
	return nil
}

func (r *recorder) LastSelfBackup(context.Context, string) (time.Time, error) { return r.last, nil }

type fixture struct {
	t       *testing.T
	db      *store.DB
	clk     *clock.Fake
	rec     *recorder
	dir     string
	secrets string
	b       *selfbackup.Backuper
	audit   *audit.Log
}

func newFixture(t *testing.T, keep int, interval time.Duration) *fixture {
	t.Helper()
	f := &fixture{t: t, db: storetest.Open(t), clk: clock.NewFake(time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)), rec: &recorder{}}
	root := t.TempDir()
	f.dir, f.secrets = filepath.Join(root, "backups"), filepath.Join(root, "secrets")
	f.audit = audit.New(f.db, audit.WithClock(f.clk.Now))
	var err error
	f.b, err = selfbackup.New(selfbackup.Config{DB: f.db, Dir: f.dir, SecretsDir: f.secrets, Keep: keep, MinInterval: interval,
		Recorder: f.rec, Audit: f.audit, Clock: f.clk})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) put(rel, data string, perm os.FileMode) {
	f.t.Helper()
	p := filepath.Join(f.secrets, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), perm); err != nil {
		f.t.Fatal(err)
	}
}

func TestBackupCopiesTheDatabaseAndSecrets(t *testing.T) {
	f := newFixture(t, 7, 0)
	ctx := context.Background()
	if err := f.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('alice', 'h', 1, 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.put("konata-token", "tok-1", 0o640)
	f.put("webhook", "hook-1", 0o600)
	f.put("not a secret name", "x", 0o600)
	f.put("big", strings.Repeat("x", 70<<10), 0o600)
	f.put("sub/inner", "nested", 0o600)
	if err := os.Symlink("/etc/passwd", filepath.Join(f.secrets, "link")); err != nil {
		t.Fatal(err)
	}

	res, err := f.b.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := filepath.Join(f.dir, res.BackupID)
	for p, mode := range map[string]os.FileMode{f.dir: 0o700, set: 0o700, set + "/lcm.db": 0o600, set + "/manifest.json": 0o600,
		set + "/secrets": 0o700, set + "/secrets/konata-token": 0o600, set + "/secrets/webhook": 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want %04o", p, fi, err, mode)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(set, "secrets"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"konata-token", "webhook"}) {
		t.Errorf("copied secrets %v", names)
	}
	raw, err := os.ReadFile(filepath.Join(set, "manifest.json")) //nolint:gosec // a file of the test's own tree
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != res.ManifestSHA256 {
		t.Error("the recorded checksum is not the manifest's")
	}
	var m struct {
		Schema int `json:"schema"`
		Files  []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want, _ := store.SchemaVersion()
	if m.Schema != want || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	var total int64
	for _, e := range m.Files {
		data, _ := os.ReadFile(filepath.Join(set, e.Name)) //nolint:gosec // a file of the test's own tree
		s := sha256.Sum256(data)
		if int64(len(data)) != e.Size || hex.EncodeToString(s[:]) != e.SHA256 {
			t.Errorf("%s does not match the manifest", e.Name)
		}
		total += e.Size
	}
	if total != res.Size {
		t.Errorf("size %d, files %d", res.Size, total)
	}

	// The copy is a working database with the data of the moment.
	cp, err := sql.Open("sqlite", "file:"+filepath.Join(set, "lcm.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cp.Close() }()
	var n int
	if err := cp.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'alice'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("copy: %d %v", n, err)
	}

	if len(f.rec.rows) != 1 {
		t.Fatalf("rows %+v", f.rec.rows)
	}
	r := f.rec.rows[0]
	if r.Resource != "lcm" || r.Kind != catalog.BackupKindSelf || r.BackupID != res.BackupID || r.Outcome != catalog.OutcomeSucceeded ||
		r.Size != res.Size || r.ManifestSHA256 != res.ManifestSHA256 || r.Actor.Name != "lcm" {
		t.Errorf("record %+v", r)
	}
	entriesAudit, err := f.audit.Search(ctx, audit.Query{Action: "lcm.backup"})
	if err != nil || len(entriesAudit) != 1 || entriesAudit[0].Target != "lcm" || entriesAudit[0].Outcome != audit.OutcomeOK {
		t.Errorf("audit %+v %v", entriesAudit, err)
	}
	for _, e := range entriesAudit {
		if strings.Contains(string(mustJSON(t, e)), "tok-1") {
			t.Error("a secret reached the audit log")
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBackupWithoutSecrets(t *testing.T) {
	f := newFixture(t, 7, 0)
	res, err := f.b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, res.BackupID, "lcm.db")); err != nil {
		t.Fatal(err)
	}
}

func TestBackupIsDailyAndKeepsTheNewestPerDay(t *testing.T) {
	f := newFixture(t, 3, 20*time.Hour)
	ctx := context.Background()
	var ids []string
	run := func() selfbackup.Result {
		t.Helper()
		res, err := f.b.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.BackupID)
		return res
	}
	run()
	// Not again within the interval.
	f.clk.Advance(time.Hour)
	if _, err := f.b.Run(ctx); !errors.Is(err, selfbackup.ErrTooSoon) {
		t.Fatalf("second run = %v", err)
	}
	for range 5 {
		f.clk.Advance(24 * time.Hour)
		run()
	}
	// Two backups on the same day: only the newest of the day stays.
	f.clk.Advance(21 * time.Hour)
	run()
	f.clk.Advance(2 * time.Hour)
	if _, err := f.b.Run(ctx); !errors.Is(err, selfbackup.ErrTooSoon) {
		t.Fatalf("run = %v", err)
	}

	entries, _ := os.ReadDir(f.dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	if len(left) != 3 || left[2] != ids[len(ids)-1] {
		t.Fatalf("left %v of %v", left, ids)
	}
	if len(f.rec.pruned) != len(ids)-3 {
		t.Errorf("pruned %v", f.rec.pruned)
	}
	for _, id := range f.rec.pruned {
		if slices.Contains(left, id) {
			t.Errorf("%s was marked pruned but exists", id)
		}
	}

	// A leftover of an unfinished backup is removed with the next prune.
	stale := filepath.Join(f.dir, "20260101T000000Z-deadbeef.tmp")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.dir, "notes")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(24 * time.Hour)
	run()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Error("the unfinished backup stayed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a directory that is not a backup was removed")
	}
}

func TestBackupDirectoryIsPrivateAndNotALink(t *testing.T) {
	f := newFixture(t, 7, 0)
	if err := os.Mkdir(f.dir, 0o755); err != nil { //nolint:gosec // a directory that is too open, to be closed
		t.Fatal(err)
	}
	if _, err := f.b.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(f.dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", fi.Mode())
	}

	g := newFixture(t, 7, 0)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, g.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := g.b.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
	if names, _ := os.ReadDir(elsewhere); len(names) != 0 {
		t.Errorf("the link's target holds %v", names)
	}
	// The failure is recorded and audited.
	if len(g.rec.rows) != 1 || g.rec.rows[0].Outcome != catalog.OutcomeFailed || g.rec.rows[0].Kind != catalog.BackupKindSelf {
		t.Errorf("rows %+v", g.rec.rows)
	}
	entries, err := g.audit.Search(context.Background(), audit.Query{Action: "lcm.backup"})
	if err != nil || len(entries) != 1 || entries[0].Outcome != audit.OutcomeError {
		t.Errorf("audit %+v %v", entries, err)
	}
}
