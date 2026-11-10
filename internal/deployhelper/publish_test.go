package deployhelper

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// publishEnv is a host whose service web publishes client/dist of its
// releases to /opt/web/www, which its route serves.
func publishEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	g := e.cfg.Services["web"]
	g.Route = &hostsetup.RouteGrant{Prefix: "/web/", Static: hostsetup.ServicePublishDir("web")}
	e.cfg.Services["web"] = g
	e.mkdir("/opt/web/www", 0o755)
	return e
}

// release builds and installs a release whose dist directory holds files
// (path -> content) and links (path -> target).
func (e *env) release(commit string, files, links map[string]string) string {
	e.t.Helper()
	e.build = func(src string) (string, int) {
		e.mkdirReal(filepath.Join(src, "dist"))
		for p, data := range files {
			e.mkdirReal(filepath.Dir(filepath.Join(src, "dist", p)))
			e.writeReal(filepath.Join(src, "dist", p), data, 0o644)
		}
		for p, target := range links {
			e.mkdirReal(filepath.Dir(filepath.Join(src, "dist", p)))
			if err := os.Symlink(target, filepath.Join(src, "dist", p)); err != nil {
				e.t.Fatal(err)
			}
		}
		return "", 0
	}
	e.checkout(commit)
	b := e.ok(buildReq(commit))
	return e.ok(Request{Op: OpInstall, Service: "web", BuildID: b.Build.BuildID, Artifact: "dist"}).Install.ReleaseID
}

func (e *env) current() string {
	target, _ := os.Readlink(e.real("/opt/web/current"))
	return strings.TrimPrefix(target, "releases/")
}

func (e *env) mtime(p string) time.Time {
	e.t.Helper()
	fi, err := os.Lstat(e.real(p))
	if err != nil {
		e.t.Fatal(err)
	}
	return fi.ModTime()
}

// writeLog records the files the helper writes, in order, and can fail
// one of them.
type writeLog struct {
	hostsetup.OS
	mu     sync.Mutex
	writes []string
	fail   string
}

func (w *writeLog) WriteFile(name string, r io.Reader, perm fs.FileMode, uid, gid int) error {
	w.mu.Lock()
	w.writes = append(w.writes, name)
	fail := w.fail != "" && strings.HasSuffix(name, w.fail)
	w.mu.Unlock()
	if fail {
		return errors.New("disk full")
	}
	return w.OS.WriteFile(name, r, perm, uid, gid)
}

func (w *writeLog) take(prefix string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, p := range w.writes {
		if rel, ok := strings.CutPrefix(p, prefix); ok {
			out = append(out, rel)
		}
	}
	w.writes = nil
	return out
}

var site = Publish{Dir: "client", Retain: []string{"assets/"}}

func activateSite(rel string) Request {
	return Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"client/server.js"}, Runtime: "node24", Publish: &site}
}

func TestPublishWritesPagesLastAndRetainsAssets(t *testing.T) {
	e := publishEnv(t)
	log := &writeLog{OS: hostsetup.OS{Root: e.root}}
	e.h.FS = log
	rel1 := e.release(commit1, map[string]string{
		"client/server.js":         "server",
		"client/index.html":        "<script src=assets/app-1.js>",
		"client/assets/app-1.js":   "one",
		"client/docs/guide.html":   "guide 1",
		"client/robots.txt":        "robots",
		"client/old.txt":           "old",
		"client/.env":              "SECRET=1",
		"client/.import-state/lck": "",
		"client/assets/.cache/x":   "x",
	}, nil)
	r := e.ok(activateSite(rel1))
	if p := r.Activate.Published; p == nil || *p != (PublishResult{Written: 6, Skipped: 3}) {
		t.Fatalf("published %+v", r.Activate.Published)
	}
	writes := log.take("/opt/web/www/")
	// The temporary names of WriteFile are not seen here; the order is.
	pages := slices.IndexFunc(writes, func(p string) bool { return strings.HasSuffix(p, ".html") })
	if pages < 0 || slices.ContainsFunc(writes[pages:], func(p string) bool { return !strings.HasSuffix(p, ".html") }) {
		t.Fatalf("pages are not written last: %q", writes)
	}
	for p, want := range map[string]string{"index.html": "<script src=assets/app-1.js>", "assets/app-1.js": "one", "docs/guide.html": "guide 1", "server.js": "server"} {
		if got := e.read("/opt/web/www/" + p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	for _, p := range []string{".env", ".import-state", "assets/.cache"} {
		if e.exists("/opt/web/www/" + p) {
			t.Errorf("%s was published", p)
		}
	}
	for p, want := range map[string]fs.FileMode{"/opt/web/www/assets": 0o755, "/opt/web/www/index.html": 0o644} {
		if fi, err := os.Lstat(e.real(p)); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	if got := e.mtime("/opt/web/www/assets/app-1.js"); !got.Equal(e.clock.Now()) {
		t.Errorf("mtime %v, want the publish time %v", got, e.clock.Now())
	}

	// The next release: app-1.js stays, being in assets/; old.txt goes.
	e.clock.Advance(24 * time.Hour)
	rel2 := e.release(commit2, map[string]string{
		"client/server.js":       "server",
		"client/index.html":      "<script src=assets/app-2.js>",
		"client/assets/app-2.js": "two",
		"client/docs/guide.html": "guide 1",
		"client/robots.txt":      "robots 2",
	}, nil)
	// A leftover hidden file of the publish directory goes too.
	e.write("/opt/web/www/assets/.app.js.lcm-123", "partial", 0o644)
	r = e.ok(activateSite(rel2))
	if p := r.Activate.Published; *p != (PublishResult{Written: 1, Replaced: 2, Unchanged: 2, Deleted: 2, Retained: 1}) {
		t.Fatalf("published %+v", p)
	}
	if writes := log.take("/opt/web/www/"); !slices.Equal(writes, []string{"assets/app-2.js", "robots.txt", "index.html"}) {
		t.Errorf("writes %q", writes)
	}
	if e.read("/opt/web/www/assets/app-1.js") != "one" || e.exists("/opt/web/www/old.txt") || e.exists("/opt/web/www/assets/.app.js.lcm-123") {
		t.Error("retention or removal is wrong")
	}
	// Unchanged files count from their last release too.
	if got := e.mtime("/opt/web/www/docs/guide.html"); !got.Equal(e.clock.Now()) {
		t.Errorf("unchanged file mtime %v", got)
	}

	// A rollback publishes the earlier release again; the newer asset
	// stays while it is young.
	e.clock.Advance(time.Hour)
	r = e.ok(activateSite(rel1))
	if e.read("/opt/web/www/index.html") != "<script src=assets/app-1.js>" || e.read("/opt/web/www/assets/app-2.js") != "two" || e.read("/opt/web/www/old.txt") != "old" {
		t.Error("the rollback did not publish the earlier release")
	}
	if p := r.Activate.Published; p.Retained != 1 || e.current() != rel1 {
		t.Errorf("published %+v, current %s", p, e.current())
	}

	// retainDays after its last release an asset goes.
	e.clock.Advance(14 * 24 * time.Hour)
	r = e.ok(activateSite(rel2))
	if e.exists("/opt/web/www/assets/app-1.js") || r.Activate.Published.Retained != 0 {
		t.Errorf("an asset outlived retainDays: %+v", r.Activate.Published)
	}
	custom := Publish{Dir: "client", Retain: []string{"assets/"}, RetainDays: 30}
	e.clock.Advance(time.Hour)
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel1, Start: []string{"client/server.js"}, Runtime: "node24", Publish: &custom})
	e.clock.Advance(20 * 24 * time.Hour)
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"client/server.js"}, Runtime: "node24", Publish: &custom})
	if !e.exists("/opt/web/www/assets/app-1.js") {
		t.Error("retainDays 30 removed a file of 20 days")
	}
}

func TestPublishRefusesLinksAndSwitchesNothingOnFailure(t *testing.T) {
	e := publishEnv(t)
	log := &writeLog{OS: hostsetup.OS{Root: e.root}}
	e.h.FS = log
	rel1 := e.release(commit1, map[string]string{"client/server.js": "s", "client/index.html": "one", "client/a.js": "a1"}, nil)
	e.ok(activateSite(rel1))

	// A link anywhere in the content refuses the publish, even one that
	// stays inside the release.
	rel2 := e.release(commit2, map[string]string{"client/server.js": "s", "client/index.html": "two"}, map[string]string{"client/sub/link.js": "../index.html"})
	e.refused(activateSite(rel2), CodeFailed, "sub/link.js is a symbolic link; publish refuses links")
	if e.current() != rel1 || e.read("/opt/web/www/index.html") != "one" {
		t.Fatalf("a refused publish changed something: current %s", e.current())
	}
	// So does a link on the way to the published directory.
	rel3 := e.release(commit2, map[string]string{"real/server.js": "s", "real/index.html": "three"}, map[string]string{"client": "real"})
	e.refused(activateSite(rel3), CodeFailed, "client is a symbolic link")
	missing := Publish{Dir: "nope"}
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel3, Start: []string{"real/server.js"}, Runtime: "node24", Publish: &missing},
		CodeFailed, "the release has no directory nope")

	// A page that cannot be written leaves the old one; the new assets
	// were written but nothing names them, and nothing is switched.
	rel4 := e.release(commit2, map[string]string{"client/server.js": "s", "client/index.html": "four", "client/a.js": "a4"}, nil)
	log.fail = "/www/index.html"
	before := len(e.systemctlCalls())
	r := e.refused(activateSite(rel4), CodeFailed, "publish index.html: disk full; nothing was switched")
	if r.Activate != nil || e.current() != rel1 || e.read("/opt/web/www/index.html") != "one" || e.read("/opt/web/www/a.js") != "a4" {
		t.Fatalf("failed publish: %+v, current %s, index %q", r.Activate, e.current(), e.read("/opt/web/www/index.html"))
	}
	if got := e.systemctlCalls()[before:]; len(got) != 0 {
		t.Errorf("systemctl after a failed publish: %q", got)
	}

	// The publish directory must be root's alone.
	log.fail = ""
	if err := os.Chmod(e.real("/opt/web/www"), 0o775); err != nil { //nolint:gosec // a test of a writable directory
		t.Fatal(err)
	}
	e.refused(activateSite(rel4), CodeFailed, "the publish directory cannot be used")
}

func TestPublishNeedsTheGrantedPublishDirectory(t *testing.T) {
	e := publishEnv(t)
	rel := e.release(commit1, map[string]string{"client/server.js": "s", "client/index.html": "one"}, nil)
	g := e.cfg.Services["web"]
	g.Route = &hostsetup.RouteGrant{Prefix: "/web/", Static: "/srv/www"}
	e.cfg.Services["web"] = g
	e.refused(activateSite(rel), CodeRefused, "publish needs services.web.route.static to be /opt/web/www")
	g.Route = nil
	e.cfg.Services["web"] = g
	e.refused(activateSite(rel), CodeRefused, "publish needs services.web.route.static")
	// A request without start needs a grant without a port.
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Publish: &site}, CodeRefused, "has the port 3200")
}

func TestStaticOnlyServiceHasNoUnit(t *testing.T) {
	e := publishEnv(t)
	g := e.cfg.Services["web"]
	g.Port = 0
	e.cfg.Services["web"] = g
	rel1 := e.release(commit1, map[string]string{"client/index.html": "one", "client/style.css": "css"}, nil)
	static := Request{Op: OpActivate, Service: "web", ReleaseID: rel1, Publish: &site, KeepReleases: 2}
	r := e.ok(static)
	if a := r.Activate; !a.Switched || a.UnitChanged || a.Published.Written != 2 || e.current() != rel1 {
		t.Fatalf("activate %+v", a)
	}
	if e.exists("/etc/systemd/system/web.service") || len(e.systemctlCalls()) != 0 {
		t.Errorf("a static-only service got a unit or systemctl calls: %q", e.systemctlCalls())
	}
	e.clock.Advance(time.Minute)
	rel2 := e.release(commit2, map[string]string{"client/index.html": "two"}, nil)
	static.ReleaseID = rel2
	if r := e.ok(static); r.Activate.Previous != rel1 || e.read("/opt/web/www/index.html") != "two" || e.exists("/opt/web/www/style.css") {
		t.Errorf("second activate %+v", r.Activate)
	}
	// A process needs a port.
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"index.html"}, Publish: &site}, CodeRefused, "has no port")
	if st := e.call(Request{Op: OpStatus, Service: "web"}); st.Code != CodeNotFound {
		t.Errorf("status of a static-only service: %+v", st)
	}
}

func TestUnitLimits(t *testing.T) {
	e := newEnv(t)
	_, rel := e.deploy(commit1)
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24",
		Limits: &UnitLimits{MemoryHigh: "768M", MemoryMax: "1G", StopTimeoutSec: 60}})
	unit := e.read("/etc/systemd/system/web.service")
	if !strings.Contains(unit, "\nRestartSec=5\nMemoryHigh=768M\nMemoryMax=1G\nTimeoutStopSec=60\n") {
		t.Fatalf("unit:\n%s", unit)
	}
	e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel, Start: []string{"index.js"}, Runtime: "node24", Limits: &UnitLimits{MemoryMax: "2G"}})
	if unit := e.read("/etc/systemd/system/web.service"); !strings.Contains(unit, "\nMemoryMax=2G\n") || strings.Contains(unit, "MemoryHigh") || strings.Contains(unit, "TimeoutStopSec") {
		t.Fatalf("unit:\n%s", unit)
	}
}
