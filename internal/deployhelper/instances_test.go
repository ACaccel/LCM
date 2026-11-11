package deployhelper

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// fakeSystemd keeps the enablement and state of several units for the
// systemctl commands of a service granted instances.
type fakeSystemd struct {
	mu      sync.Mutex
	enabled map[string]bool
	active  map[string]string
	calls   []string
	// restartFail fails restart with status 1 and marks this unit failed.
	restartFail string
}

func (s *fakeSystemd) handle(c execx.Cmd) (execx.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := func(out string, code int) (execx.Result, error) {
		if code != 0 {
			return execx.Result{Output: out, ExitCode: code}, &execx.ExitError{Code: code}
		}
		return execx.Result{Output: out}, nil
	}
	s.calls = append(s.calls, strings.Join(c.Argv[1:], " "))
	var units []string
	for _, a := range c.Argv[2:] {
		if !strings.HasPrefix(a, "-") {
			units = append(units, a)
		}
	}
	code := 0
	var out []string
	switch c.Argv[1] {
	case "is-enabled":
		for _, u := range units {
			if !s.enabled[u] {
				out, code = append(out, "disabled"), 1
				continue
			}
			out = append(out, "enabled")
		}
	case "is-active":
		for _, u := range units {
			st := s.active[u]
			if st == "" {
				st = "inactive"
			}
			if st != "active" {
				code = 3
			}
			out = append(out, st)
		}
	case "enable":
		for _, u := range units {
			s.enabled[u] = true
			if slices.Contains(c.Argv, "--now") {
				s.active[u] = "active"
			}
		}
	case "disable":
		for _, u := range units {
			s.enabled[u] = false
			if slices.Contains(c.Argv, "--now") {
				s.active[u] = "inactive"
			}
		}
	case "start", "restart":
		for _, u := range units {
			s.active[u] = "active"
			if c.Argv[1] == "restart" && u == s.restartFail {
				s.active[u], code = "failed", 1
			}
		}
		if code != 0 {
			return result("Job for "+s.restartFail+" failed.\n", 1)
		}
	case "stop":
		for _, u := range units {
			s.active[u] = "inactive"
		}
	case "show":
		if c.Argv[2] == "--property=NeedDaemonReload" {
			for range units {
				out = append(out, "no\n")
			}
			return result(strings.Join(out, "\n"), 0)
		}
		for _, u := range units {
			st := s.active[u]
			if st == "" {
				st = "inactive"
			}
			file := "disabled"
			if s.enabled[u] {
				file = "enabled"
			}
			out = append(out, "Id="+u+"\nLoadState=loaded\nActiveState="+st+"\nSubState=running\nUnitFileState="+file+"\nMainPID=7\n")
		}
		return result(strings.Join(out, "\n"), 0)
	}
	return result(strings.Join(out, "\n")+"\n", code)
}

func (s *fakeSystemd) since(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls[n:])
}

func (s *fakeSystemd) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// instancesEnv is a host whose service web is granted the instances a
// (port 3201), b (no port, the group shared) and c (port 3203).
func instancesEnv(t *testing.T) (*env, *fakeSystemd) {
	t.Helper()
	e := newEnv(t)
	sd := &fakeSystemd{enabled: map[string]bool{}, active: map[string]string{}}
	runner := &execx.Fake{}
	runner.On(func(c execx.Cmd) bool { return c.Argv[0] == systemctl }, sd.handle)
	runner.On(func(execx.Cmd) bool { return true }, e.handle)
	e.h.Runner = runner
	e.h.LookupGroup = func(_ context.Context, name string) (hostsetup.Group, bool, error) {
		if name == "shared" {
			return hostsetup.Group{Name: "shared", GID: 982}, true, nil
		}
		return hostsetup.Group{}, false, nil
	}
	e.cfg.Services["web"] = hostsetup.ServiceGrant{Runtimes: []string{"node24"}, After: []string{"db.service"}, Wants: []string{"db.service"},
		Instances: map[string]hostsetup.InstanceGrant{"a": {Port: 3201}, "b": {Groups: []string{"shared"}}, "c": {Port: 3203}}}
	for _, in := range []string{"a", "b", "c"} {
		e.write(hostsetup.ServiceInstanceEnvFile("web", in), "", 0o640)
	}
	return e, sd
}

func instances(names ...string) []InstanceSpec {
	var out []InstanceSpec
	for _, n := range names {
		out = append(out, InstanceSpec{Name: n, Start: []string{"index.js", "--bot=" + n}})
	}
	return out
}

func (e *env) installed(commit string) string {
	e.t.Helper()
	e.checkout(commit)
	b := e.ok(buildReq(commit))
	return e.ok(Request{Op: OpInstall, Service: "web", BuildID: b.Build.BuildID, Artifact: "dist"}).Install.ReleaseID
}

func activateInstances(rel string, in []InstanceSpec) Request {
	return Request{Op: OpActivate, Service: "web", ReleaseID: rel, Runtime: "node24", Instances: in}
}

func TestInstanceUnitGolden(t *testing.T) {
	for _, tc := range []struct{ file, got string }{
		{"testdata/unit-single.golden", renderUnit("web-app", 3001, []string{"/opt/lcm/runtimes/node24/bin/node", "server/index.js", "--prod"},
			"/opt/lcm/runtimes/node24/bin", "/var/backups/lcm-services", &UnitLimits{MemoryHigh: "384M", MemoryMax: "512M", StopTimeoutSec: 30})},
		{"testdata/unit-plain.golden", renderUnit("acaccel-site", 4200, []string{"/opt/acaccel-site/current/bin/site"}, "", "", nil)},
		{"testdata/unit-instance.golden", renderSpec(unitSpec{svc: "bots", instance: "alpha", port: 4100,
			execStart: []string{"/opt/lcm/runtimes/node24/bin/node", "dist/bot/alpha/index.js"}, rtBin: "/opt/lcm/runtimes/node24/bin",
			backup: "/var/backups/lcm-services", limits: &UnitLimits{MemoryHigh: "384M", MemoryMax: "512M", StopTimeoutSec: 60},
			after: []string{"mongodb-bots.service"}, wants: []string{"mongodb-bots.service"}})},
		{"testdata/unit-instance-noport.golden", renderSpec(unitSpec{svc: "bots", instance: "log-archive",
			execStart: []string{"/opt/lcm/runtimes/node24/bin/node", "dist/bot/log-archive/index.js"}, rtBin: "/opt/lcm/runtimes/node24/bin",
			limits: &UnitLimits{MemoryMax: "1G"}, groups: []string{"bots-db"}})},
	} {
		want, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		if tc.got != string(want) {
			t.Errorf("%s differs:\n%s", tc.file, tc.got)
		}
	}
	// The grant's groups, after and wants reach a single unit too.
	u := renderSpec(unitSpec{svc: "web", port: 3200, execStart: []string{"x"}, after: []string{"db.service"}, wants: []string{"db.service"}, groups: []string{"g"}})
	for _, want := range []string{"\nAfter=network.target db.service\nWants=db.service\n", "\nGroup=web\nSupplementaryGroups=g\n", "\nSocketBindAllow=tcp:3200\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if strings.Contains(u, "LCM_INSTANCE") {
		t.Errorf("single unit names an instance:\n%s", u)
	}
}

func TestActivateInstances(t *testing.T) {
	e, sd := instancesEnv(t)
	// A single unit from before the service had instances.
	e.write(hostsetup.ServiceUnit("web"), hostsetup.ServiceUnitMarker+"\n[Unit]\n", 0o644)
	sd.enabled["web.service"], sd.active["web.service"] = true, "active"
	rel1 := e.installed(commit1)
	n := sd.count()
	r := e.ok(activateInstances(rel1, instances("a", "b", "c")))
	want := []string{
		"daemon-reload",
		"disable --now --quiet web.service",
		"enable --quiet web@a.service web@b.service web@c.service",
		"is-enabled web@a.service web@b.service web@c.service",
		"restart web@a.service web@b.service web@c.service",
		"show --property=Id,LoadState,ActiveState,SubState,UnitFileState,MainPID web@a.service web@b.service web@c.service",
		"daemon-reload",
	}
	if got := sd.since(n); !slices.Equal(got, want) {
		t.Fatalf("systemctl %q\nwant %q", got, want)
	}
	if e.exists(hostsetup.ServiceUnit("web")) || !slices.Equal(r.Activate.Removed, []string{""}) || !r.Activate.Switched || !r.Activate.UnitChanged {
		t.Fatalf("activate %+v", r.Activate)
	}
	for i, in := range r.Activate.Instances {
		if !in.Enabled || !in.Restarted || !in.UnitChanged || in.ActiveState != "active" || in.Name != []string{"a", "b", "c"}[i] {
			t.Errorf("instance %+v", in)
		}
	}
	a := e.read(hostsetup.InstanceUnit("web", "a"))
	b := e.read(hostsetup.InstanceUnit("web", "b"))
	for _, want := range []string{"EnvironmentFile=/etc/web/env\nEnvironmentFile=/etc/web/a.env\nEnvironment=LCM_SERVICE=web LCM_INSTANCE=a\nEnvironment=PORT=3201 HOST=127.0.0.1\n",
		"ExecStart=/opt/lcm/runtimes/node24/bin/node index.js --bot=a\n", "SocketBindAllow=tcp:3201\n", "After=network.target db.service\nWants=db.service\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("unit a lacks %q:\n%s", want, a)
		}
	}
	if strings.Contains(b, "PORT=") || strings.Contains(b, "SocketBindAllow") || !strings.Contains(b, "SupplementaryGroups=shared\n") || !strings.Contains(b, "SocketBindDeny=any\n") {
		t.Errorf("unit b:\n%s", b)
	}

	// Status aggregates the enabled instances and lists each.
	st := e.ok(Request{Op: OpStatus, Service: "web"})
	if st.Unit.ActiveState != "active" || st.Unit.Release != rel1 || len(st.Instances) != 3 || st.Instances[0].Port != 3201 || !st.Instances[1].Granted {
		t.Fatalf("status %+v %+v", st.Unit, st.Instances)
	}

	// An instance stopped by name stays stopped: deploys and service-wide
	// actions leave it alone.
	e.ok(Request{Op: OpStop, Service: "web", Instance: "b"})
	if sd.enabled["web@b.service"] || sd.active["web@b.service"] != "inactive" {
		t.Fatal("b was not disabled and stopped")
	}
	st = e.ok(Request{Op: OpStatus, Service: "web"})
	if st.Unit.ActiveState != "active" || st.Instances[1].UnitFileState != "disabled" {
		t.Fatalf("status after stop %+v %+v", st.Unit, st.Instances)
	}
	rel2 := e.installed(commit2)
	n = sd.count()
	r = e.ok(activateInstances(rel2, instances("a", "b", "c")))
	if got := sd.since(n); !slices.Contains(got, "restart web@a.service web@c.service") || slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "enable") }) {
		t.Fatalf("systemctl %q", got)
	}
	if in := r.Activate.Instances[1]; in.Enabled || in.Restarted || in.UnitChanged {
		t.Errorf("stopped instance %+v", in)
	}
	n = sd.count()
	e.ok(Request{Op: OpRestart, Service: "web"})
	e.ok(Request{Op: OpStart, Service: "web"})
	if got := sd.since(n); !slices.Contains(got, "restart web@a.service web@c.service") || !slices.Contains(got, "start web@a.service web@c.service") {
		t.Fatalf("service-wide actions %q", got)
	}

	// The last enabled instance cannot be stopped by name.
	e.ok(Request{Op: OpStop, Service: "web", Instance: "a"})
	e.refused(Request{Op: OpStop, Service: "web", Instance: "c"}, CodeRefused, "c is the last enabled instance of web; stop the service instead")
	n = sd.count()
	e.ok(Request{Op: OpStart, Service: "web", Instance: "b"})
	e.ok(Request{Op: OpRestart, Service: "web", Instance: "a"})
	if got := sd.since(n); !slices.Equal(got, []string{"enable --now --quiet web@b.service", "enable --quiet web@a.service", "restart web@a.service"}) {
		t.Fatalf("instance actions %q", got)
	}
	n = sd.count()
	e.ok(Request{Op: OpStop, Service: "web"})
	if got := sd.since(n); !slices.Equal(got, []string{"stop web@a.service web@b.service web@c.service"}) {
		t.Fatalf("service stop %q", got)
	}

	// An instance no longer requested is stopped before the others restart
	// and its unit removed.
	e.ok(Request{Op: OpStart, Service: "web"})
	n = sd.count()
	r = e.ok(activateInstances(rel1, instances("a", "b")))
	got := sd.since(n)
	if i, j := slices.Index(got, "disable --now --quiet web@c.service"), slices.Index(got, "restart web@a.service web@b.service"); i < 0 || j < i {
		t.Fatalf("systemctl %q", got)
	}
	if e.exists(hostsetup.InstanceUnit("web", "c")) || !slices.Equal(r.Activate.Removed, []string{"c"}) {
		t.Fatalf("orphan c: %+v", r.Activate)
	}

	// Unit operations on instances that are unknown or have no unit.
	e.refused(Request{Op: OpRestart, Service: "web", Instance: "zz"}, CodeRefused, "the instance zz is not granted")
	e.refused(Request{Op: OpRestart, Service: "web", Instance: "c"}, CodeNotFound, "has no unit yet")
}

func TestActivateInstancesFailures(t *testing.T) {
	e, sd := instancesEnv(t)
	rel1 := e.installed(commit1)
	e.ok(activateInstances(rel1, instances("a", "b", "c")))
	unitA := e.read(hostsetup.InstanceUnit("web", "a"))
	rel2 := e.installed(commit2)

	// A unit that does not verify puts back every unit and the links.
	e.analyze = 1
	r := e.refused(activateInstances(rel2, []InstanceSpec{{Name: "a", Start: []string{"bin/server"}}, {Name: "b", Start: []string{"index.js"}}, {Name: "c", Start: []string{"index.js"}}}),
		CodeFailed, "a unit does not verify; the previous units and release were restored")
	if r.Activate.Switched || e.read(hostsetup.InstanceUnit("web", "a")) != unitA {
		t.Fatalf("not restored: %+v", r.Activate)
	}
	if target, _ := os.Readlink(e.real("/opt/web/current")); target != "releases/"+rel1 {
		t.Errorf("current -> %q", target)
	}
	e.analyze = 0

	// A restart that fails leaves the release current and reports each
	// instance.
	sd.restartFail = "web@b.service"
	r = e.refused(activateInstances(rel2, instances("a", "b", "c")), CodeFailed, "systemctl restart exited with status 1")
	if !r.Activate.Switched || r.Activate.Instances[1].ActiveState != "failed" || r.Activate.Instances[0].ActiveState != "active" {
		t.Fatalf("activate %+v", r.Activate)
	}
	sd.restartFail = ""

	// Refusals before anything changes.
	single := e.cfg.Services["web"]
	e.cfg.Services["web"] = hostsetup.ServiceGrant{Port: 3200, Runtimes: []string{"node24"}}
	e.refused(activateInstances(rel2, instances("a")), CodeRefused, "but services.web has none")
	e.cfg.Services["web"] = single
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel2, Start: []string{"index.js"}, Runtime: "node24"}, CodeRefused, "but the request has none")
	e.refused(activateInstances(rel2, instances("a", "zz")), CodeRefused, "the instance zz is not granted to web")
	e.write("/etc/web/b.env", "LCM_INSTANCE=x\n", 0o640)
	e.refused(activateInstances(rel2, instances("a", "b")), CodeRefused, "/etc/web/b.env sets LCM_INSTANCE")
	e.write("/etc/web/b.env", "PORT=1\n", 0o640)
	e.refused(activateInstances(rel2, instances("a", "b")), CodeRefused, "/etc/web/b.env sets PORT")
	e.write("/etc/web/b.env", "", 0o640)
	e.h.LookupGroup = func(context.Context, string) (hostsetup.Group, bool, error) { return hostsetup.Group{}, false, nil }
	e.refused(activateInstances(rel2, instances("a", "b")), CodeRefused, "the group shared that /etc/lcm/host.json grants does not exist")
	e.h.LookupGroup = func(context.Context, string) (hostsetup.Group, bool, error) {
		return hostsetup.Group{Name: "shared", GID: 4}, true, nil
	}
	e.refused(activateInstances(rel2, instances("a", "b")), CodeRefused, "static system group")
	e.write("/etc/systemd/system/web@x.service", "[Unit]\n", 0o644)
	e.refused(activateInstances(rel2, instances("a")), CodeRefused, "web@x.service exists and was not written by the deploy helper")
	e.refused(Request{Op: OpStatus, Service: "web"}, CodeRefused, "was not written by the deploy helper")
	if err := os.Remove(e.real("/etc/systemd/system/web@x.service")); err != nil {
		t.Fatal(err)
	}
	e.mkdir("/etc/systemd/system/web@.service.d", 0o755)
	e.refused(activateInstances(rel2, instances("a")), CodeRefused, "web@.service.d exists; a template or its drop-ins")
}

func TestQuiesceInstances(t *testing.T) {
	e, sd := instancesEnv(t)
	e.write("/var/lib/web/data/x", "x", 0o640)
	rel := e.installed(commit1)
	e.ok(activateInstances(rel, instances("a", "b", "c")))
	e.ok(Request{Op: OpStop, Service: "web", Instance: "b"})
	n := sd.count()
	e.ok(Request{Op: OpBackup, Service: "web", Sources: []BackupSource{{Type: SourceFiles, Path: "data"}}, Mode: ModeStopped, TimeoutSec: 60})
	got := sd.since(n)
	want := []string{
		"is-active web@a.service web@b.service web@c.service",
		"stop web@a.service web@b.service web@c.service",
		"is-active web@a.service web@b.service web@c.service",
		"start web@a.service web@c.service",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("systemctl %q\nwant %q", got, want)
	}
}

func TestInstanceRequests(t *testing.T) {
	id := "0b7c4a1e-3f2d-4c5b-9a8e-123456789abc"
	act := func(r Request) Request {
		r.Version, r.Op, r.OperationID, r.Service, r.ReleaseID = Version, OpActivate, id, "web", "20261109T120000Z-0a1b2c3d"
		return r
	}
	for name, tc := range map[string]struct {
		req  Request
		want string
	}{
		"instances on start":    {Request{Version: Version, Op: OpStart, OperationID: id, Service: "web", Instances: instances("a")}, "start does not take instances"},
		"instance on activate":  {act(Request{Start: []string{"x"}, Instance: "a"}), "activate does not take instance"},
		"start with instances":  {act(Request{Start: []string{"x"}, Instances: instances("a")}), "takes start and limits per instance only"},
		"limits with instances": {act(Request{Limits: &UnitLimits{MemoryMax: "1G"}, Instances: instances("a")}), "takes start and limits per instance only"},
		"unsorted":              {act(Request{Instances: instances("b", "a")}), "sorted by name, each once"},
		"duplicate":             {act(Request{Instances: instances("a", "a")}), "sorted by name, each once"},
		"empty":                 {act(Request{Instances: []InstanceSpec{}}), "between 1 and 16"},
		"bad name":              {act(Request{Instances: instances("a--b")}), "instances[0].name is not an instance name"},
		"bad start":             {act(Request{Instances: []InstanceSpec{{Name: "a", Start: []string{"/bin/sh"}}}}), "instances[0].start must begin"},
		"bad limits":            {act(Request{Instances: []InstanceSpec{{Name: "a", Start: []string{"x"}, Limits: &UnitLimits{MemoryHigh: "2G", MemoryMax: "1G"}}}}), "instances[0].limits"},
		"bad instance":          {Request{Version: Version, Op: OpRestart, OperationID: id, Service: "web", Instance: "A"}, "instance is not an instance name"},
	} {
		if err := tc.req.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	ok := act(Request{Instances: instances("a", "b"), Publish: &Publish{Dir: "www"}, Migrate: []string{"node", "m.js"}})
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	// Requests without the new fields hash as before.
	plain := Request{Version: Version, Op: OpRestart, OperationID: id, Service: "web"}
	if raw, err := json.Marshal(plain); err != nil || string(raw) != `{"version":1,"op":"restart","operationId":"`+id+`","service":"web"}` {
		t.Fatalf("a plain request encodes as %s (%v)", raw, err)
	}
	for _, raw := range []string{
		`{"version":1,"op":"status","operationId":"` + id + `","service":"web","instance":"a"}`,
		`{"version":1,"op":"activate","operationId":"` + id + `","service":"web","releaseId":"20261109T120000Z-0a1b2c3d","instances":[{"name":"a","start":["x"]}]}`,
	} {
		if _, err := DecodeRequest([]byte(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
