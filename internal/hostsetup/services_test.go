package hostsetup

import (
	"io/fs"
	"strings"
	"testing"
)

const testInput = "/srv/inputs/web/assets"

func TestDeployConfig(t *testing.T) {
	base := `"operator":"alice","repository":"https://github.com/ACaccel/LCM.git",` +
		`"toolchains":{"go":"/g","node":"/n"},"tailscale":{"port":9443},"components":{"deploy":true}`
	c, err := ParseConfig([]byte(`{` + base + `,
		"runtimes":{"node24":"/home/alice/.local/node"},
		"backup":{"dir":"/var/backups/lcm-services"},
		"services":{"web":{"port":3200,"runtimes":["node24"],"inputs":{"assets":"/srv/inputs/web/assets"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Components.Deploy || c.Runtimes["node24"] != "/home/alice/.local/node" || c.Backup.Dir != "/var/backups/lcm-services" ||
		c.Services["web"].Port != 3200 || c.Services["web"].Inputs["assets"] != testInput {
		t.Fatalf("parsed %+v", c)
	}
	if again, err := ParseConfig(c.Encode()); err != nil || again.Services["web"].Runtimes[0] != "node24" {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	for in, want := range map[string]string{
		`"services":{"web":{"port":3200,"route":"/web/"}}`:                    `services.web.route must be an object`,
		`"services":{"web":{"port":3200,"extraPaths":[]}}`:                    `"services.web.extraPaths" is reserved`,
		`"services":{"web":{"port":3200,"Port":3201}}`:                        `unknown key "services.web.Port"`,
		`"services":{"web":{"port":3200},"web":{"port":3201}}`:                `duplicate key "services.web"`,
		`"services":{"lcm":{"port":3200}}`:                                    `must not start with "lcm"`,
		`"services":{"lcmb-web":{"port":3200}}`:                               `must not start with "lcm"`,
		`"services":{"abcdefghijklmnopqrstuvwxyz12":{"port":3200}}`:           `service names must match`,
		`"services":{"Web":{"port":3200}}`:                                    `service names must match`,
		`"services":{"web":{"port":80}}`:                                      "between 1024 and 65535",
		`"services":{"web":{"port":7800}}`:                                    "is LCM's own",
		`"services":{"web":{"port":9443}}`:                                    "is LCM's own",
		`"services":{"a":{"port":3200},"b":{"port":3200}}`:                    "is also the port of services.a",
		`"services":{"web":{"port":3200,"runtimes":["node24"]}}`:              `"node24" is not a runtime`,
		`"runtimes":{"Node":"/n"}`:                                            "runtimes: names must match",
		`"runtimes":{"node":"relative"}`:                                      "runtimes.node must be an absolute",
		`"runtimes":{"node":"/a b"}`:                                          "runtimes.node must be an absolute",
		`"services":{"web":{"port":3200,"inputs":{"a":"/var/lib/lcm/x"}}}`:    "must not contain or be inside /var/lib/lcm",
		`"services":{"web":{"port":3200,"inputs":{"a":"/var"}}}`:              "must not contain or be inside /var/lib/lcm",
		`"services":{"web":{"port":3200,"inputs":{"a":"/srv/a:b"}}}`:          "inputs.a must be an absolute",
		`"services":{"web":{"port":3200,"inputs":{"A":"/srv/a"}}}`:            "inputs: names must match",
		`"backup":{"dir":"/var/lib/lcm/backups"}`:                             "backup.dir must not be under /var/lib/lcm",
		`"backup":{"dir":"/srv/b","keep":1}`:                                  `unknown key "backup.keep"`,
		`"backup":{"dir":"/"}`:                                                "backup.dir must be an absolute",
		`"backup":{"dir":"/var"}`:                                             "not the system directory /var",
		`"backup":{"dir":"/var/lib"}`:                                         "not the system directory /var/lib",
		`"backup":{"dir":"/opt"}`:                                             "not the system directory /opt",
		`"backup":{"dir":"/home"}`:                                            "not the system directory /home",
		`"backup":{"dir":"/etc/backups"}`:                                     "under the system tree /etc",
		`"backup":{"dir":"/usr/local/backups"}`:                               "under the system tree /usr",
		`"backup":{"dir":"/tmp/b"}`:                                           "under the system tree /tmp",
		`"backup":{"dir":"/var/backups/lcm"}`:                                 "must not be /var/backups/lcm, where host upgrade keeps its sets; use /var/backups/lcm-services",
		`"backup":{"dir":"/var/lib/lcm-build"}`:                               "LCM's own /var/lib/lcm",
		`"backup":{"dir":"/var/lib/lcmx"}`:                                    "LCM's own /var/lib/lcm",
		`"backup":{"dir":"/var/cache/lcm-build/b"}`:                           "must not overlap /var/cache/lcm-build",
		`"backup":{"dir":"/opt/lcm/b"}`:                                       "must not overlap /opt/lcm",
		`"services":{"web":{"port":3200}},"backup":{"dir":"/var/lib/web"}`:    "must not overlap /var/lib/web",
		`"services":{"web":{"port":3200}},"backup":{"dir":"/var/lib/web/b"}`:  "must not overlap /var/lib/web",
		`"services":{"web":{"port":3200}},"backup":{"dir":"/opt/web"}`:        "must not overlap /opt/web",
		`"services":{"web":{"port":3200}},"backup":{"dir":"/opt/web/b"}`:      "must not overlap /opt/web",
		`"services":{"web":{"port":3200}},"backup":{"dir":"/etc/web"}`:        "under the system tree /etc",
		`"runtimes":{"node":"/srv/node"},"backup":{"dir":"/srv"}`:             "not the system directory /srv",
		`"runtimes":{"node":"/srv/node"},"backup":{"dir":"/srv/node/b"}`:      "must not overlap /srv/node",
		`"components":{"deploy":true,"ingress":true}`:                         `duplicate key "components"`,
		`"services":{"web":{"port":3200,"inputs":{"a":"/srv/a","a":"/srv"}}}`: `duplicate key "services.web.inputs.a"`,
	} {
		if _, err := ParseConfig([]byte(`{` + base + `,` + in + `}`)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

// deployHost returns a converged test host with the deploy component, a
// runtime and one service grant configured but not applied.
func deployHost(t *testing.T) *testHost {
	t.Helper()
	th := converged(t)
	th.fs.dir(testInput, 0o755, aliceUID, aliceUID)
	th.fs.dir("/srv", 0o755, 0, 0)
	th.fs.dir("/srv/inputs", 0o755, aliceUID, aliceUID)
	th.fs.dir("/srv/inputs/web", 0o755, aliceUID, aliceUID)
	th.cfg.Components.Deploy = true
	th.cfg.Runtimes = map[string]string{"node24": "/home/alice/.local/node"}
	th.cfg.Backup = &Backup{Dir: "/var/backups/lcm-services"}
	th.cfg.Services = map[string]ServiceGrant{"web": {Port: 3200, Runtimes: []string{"node24"}, Inputs: map[string]string{"assets": testInput}}}
	th.writeConfig(th.cfg)
	return th
}

func TestDeployComponentAndGrants(t *testing.T) {
	th := deployHost(t)
	th.fs.resetOps()
	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Manual != 0 {
		t.Fatalf("check: %+v %v\n%s", res, err, out)
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("check wrote %v", w)
	}
	for _, want := range []string{
		"deploy:          would change: install /usr/local/libexec/lcm-deploy\n",
		"service web:     would change: create the system account lcmb-web (home /var/lib/lcm-build/web, no login shell)",
		"runtime node24:  would change: copy /home/alice/.local/node to /opt/lcm/runtimes/node24 (missing)",
		"service web:     would change: create the system account web (home /var/lib/web, no login shell)",
		"service web:     would change: create an empty /etc/web/env (root:web 0640)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output lacks %q:\n%s", want, out)
		}
	}

	out = th.converge()
	build, web := th.r.accounts["lcmb-web"], th.r.accounts["web"]
	if build.Home != "/var/lib/lcm-build/web" || build.Shell != "/usr/sbin/nologin" || build.GECOS != "LCM build web" ||
		web.Home != "/var/lib/web" || web.GECOS != "LCM service web" {
		t.Fatalf("accounts %+v %+v", build, web)
	}
	for _, f := range []struct {
		path     string
		perm     fs.FileMode
		uid, gid int
		data     string
	}{
		{DeployWrapper, 0o755, 0, 0, string(asset("lcm-deploy"))},
		{DeployHelper, 0o755, 0, 0, exeData},
		{DeploySudoers, 0o440, 0, 0, string(asset("lcm-deploy-sudoers"))},
		{"/etc/web/env", 0o640, 0, web.GID, ""},
		{RuntimesDir + "/node24/bin/node", 0o755, 0, 0, "node binary"},
	} {
		n := th.fs.get(f.path)
		if n == nil || n.mode != f.perm || n.uid != f.uid || n.gid != f.gid || string(n.data) != f.data {
			t.Errorf("%s = %+v, want %04o %d:%d", f.path, n, f.perm, f.uid, f.gid)
		}
	}
	for _, d := range []struct {
		path     string
		perm     fs.FileMode
		uid, gid int
	}{
		{BuildDir, 0o755, 0, 0}, {BuildCacheDir, 0o755, 0, 0}, {DeployLedger, 0o700, 0, 0}, {"/var/backups/lcm-services", 0o700, 0, 0},
		{"/var/lib/lcm-build/web", 0o750, 0, build.GID}, {"/var/cache/lcm-build/web", 0o750, build.UID, build.GID},
		{RuntimesDir, 0o755, 0, 0}, {"/opt/web", 0o755, 0, 0}, {"/opt/web/releases", 0o755, 0, 0},
		{"/var/lib/web", 0o750, web.UID, web.GID}, {"/etc/web", 0o750, 0, web.GID},
	} {
		n := th.fs.get(d.path)
		if n == nil || n.mode != fs.ModeDir|d.perm || n.uid != d.uid || n.gid != d.gid {
			t.Errorf("%s = %+v, want dir %04o %d:%d", d.path, n, d.perm, d.uid, d.gid)
		}
	}
	// The grant comes after the files it grants, and the runtime copy is
	// checked as nobody before it replaces anything.
	if th.r.index("visudo -c") < 0 || len(th.r.called("[nobody] /opt/lcm/runtimes/node24.new/bin/node --version")) != 1 {
		t.Errorf("commands: %v", th.r.calls)
	}
	if !strings.Contains(out, "runtime node24:  note: node v24.0.0") {
		t.Errorf("no runtime version:\n%s", out)
	}
	th.expectConverged()

	// An env file with content is kept; its group and mode are fixed.
	th.fs.file("/etc/web/env", "SECRET=x\n", 0o644, 0, 0)
	th.converge()
	if n := th.fs.get("/etc/web/env"); string(n.data) != "SECRET=x\n" || n.mode != 0o640 || n.gid != web.GID {
		t.Errorf("env file %+v", n)
	}

	// A removed grant is reported and nothing is removed.
	th.cfg.Services = nil
	th.writeConfig(th.cfg)
	res, out, err = th.apply(Options{})
	if err != nil || res.Changes != 0 || !strings.Contains(out, "services:        unchanged (web is no longer in services") {
		t.Fatalf("removed grant: %+v %v\n%s", res, err, out)
	}
	if th.fs.get("/opt/web/releases") == nil || th.fs.get("/etc/web/env") == nil {
		t.Fatal("a removed grant's files were removed")
	}

	// Turning the component off keeps it.
	th.cfg.Components.Deploy = false
	th.writeConfig(th.cfg)
	if res, out, err := th.apply(Options{Check: true}); err != nil || res.Changes != 0 || !strings.Contains(out, "deploy:          unchanged (installed but disabled") {
		t.Fatalf("disabled: %+v %v\n%s", res, err, out)
	}
}

func TestGrantProblemsNeedManualAction(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(th *testHost)
		want  string
	}{
		"account LCM did not create": {func(th *testHost) {
			th.r.accounts["web"] = Account{Name: "web", UID: 120, GID: 120, Home: "/var/lib/web", Shell: "/usr/sbin/nologin"}
		}, `account web exists and was not created by LCM (its comment is not "LCM service web")`},
		"login account": {func(th *testHost) {
			th.r.accounts["web"] = Account{Name: "web", UID: 1001, GID: 1001, GECOS: "LCM service web", Home: "/var/lib/web", Shell: "/bin/bash"}
		}, "account web exists and is not a system account"},
		"system account with a shell": {func(th *testHost) {
			th.r.accounts["web"] = Account{Name: "web", UID: 120, GID: 120, GECOS: "LCM service web", Home: "/var/lib/web", Shell: "/bin/sh"}
		}, "account web exists with a login shell"},
		"system account with another home": {func(th *testHost) {
			th.r.accounts["web"] = Account{Name: "web", UID: 120, GID: 120, GECOS: "LCM service web", Home: "/var/www", Shell: "/usr/sbin/nologin"}
		}, "account web exists with home directory /var/www"},
		"build account LCM did not create": {func(th *testHost) {
			th.r.accounts["lcmb-web"] = Account{Name: "lcmb-web", UID: 121, GID: 121, Home: "/var/lib/lcm-build/web", Shell: "/usr/sbin/nologin"}
		}, "account lcmb-web exists and was not created by LCM"},
		"build directory without the account": {func(th *testHost) {
			th.fs.dir("/var/cache/lcm-build/web", 0o750, 0, 0)
		}, "/var/cache/lcm-build/web exists but the account lcmb-web does not"},
		"drop-in": {func(th *testHost) {
			th.fs.dir("/etc/systemd/system/web.service.d", 0o755, 0, 0)
		}, "the drop-in directory /etc/systemd/system/web.service.d exists"},
		"runtime control drop-in": {func(th *testHost) {
			th.fs.dir("/etc/systemd/system.control/web.service.d", 0o755, 0, 0)
		}, "the drop-in directory /etc/systemd/system.control/web.service.d exists"},
		"generated unit": {func(th *testHost) {
			th.fs.file("/run/systemd/generator/web.service", "[Service]\n", 0o644, 0, 0)
		}, "the unit /run/systemd/generator/web.service exists"},
		"transient unit": {func(th *testHost) {
			th.fs.file("/run/systemd/transient/web.service", "[Service]\n", 0o644, 0, 0)
		}, "the unit /run/systemd/transient/web.service exists"},
		"directory without the account": {func(th *testHost) {
			th.fs.dir("/etc/web", 0o755, 0, 0)
		}, "/etc/web exists but the account web does not"},
		"vendor unit": {func(th *testHost) {
			th.fs.file("/usr/lib/systemd/system/web.service", "[Service]\n", 0o644, 0, 0)
		}, "the unit /usr/lib/systemd/system/web.service exists"},
		"foreign unit": {func(th *testHost) {
			th.fs.file(UnitDir+"/web.service", "[Service]\n", 0o644, 0, 0)
		}, "/etc/systemd/system/web.service exists and was not written by the deploy helper"},
	} {
		t.Run(name, func(t *testing.T) {
			th := deployHost(t)
			tc.setup(th)
			res, out, err := th.apply(Options{})
			if err != nil || res.Manual != 1 || !strings.Contains(out, "service web:     needs manual action: "+tc.want) {
				t.Fatalf("%+v %v\n%s", res, err, out)
			}
			if th.fs.get("/var/lib/web") != nil {
				t.Fatal("the service's directories were created")
			}
		})
	}

	t.Run("directory of another owner", func(t *testing.T) {
		th := deployHost(t)
		th.converge()
		th.fs.get("/var/lib/web").uid = aliceUID
		res, out, err := th.apply(Options{})
		if err != nil || res.Manual != 1 || !strings.Contains(out, "/var/lib/web is owned by 1000:") || th.fs.get("/var/lib/web").uid != aliceUID {
			t.Fatalf("%+v %v\n%s", res, err, out)
		}
	})
	t.Run("own unit", func(t *testing.T) {
		th := deployHost(t)
		th.fs.file(UnitDir+"/web.service", ServiceUnitMarker+"\n[Service]\n", 0o644, 0, 0)
		if res, out, err := th.apply(Options{}); err != nil || res.Manual != 0 {
			t.Fatalf("%+v %v\n%s", res, err, out)
		}
	})
}

// A file a service keeps open for writing can grow past the limits of a
// backup, which only the same file system or a quota bounds.
func TestApplyWarnsWhenBackupsAreOnAnotherFilesystem(t *testing.T) {
	th := deployHost(t)
	th.fs.dir("/var/backups/lcm-services", 0o700, 0, 0)
	th.fs.get("/var/backups/lcm-services").dev = 42
	if _, out, err := th.apply(Options{}); err != nil || !strings.Contains(out, "backup.dir is on another file system than /var/lib") {
		t.Errorf("%v\n%s", err, out)
	}
	th = deployHost(t)
	th.fs.dir("/var/backups/lcm-services", 0o700, 0, 0)
	if _, out, err := th.apply(Options{}); err != nil || strings.Contains(out, "another file system") {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestUntrustedDeploySourcesAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(th *testHost)
		want  string
	}{
		"input of another user": {func(th *testHost) { th.fs.get(testInput).uid = 1234 }, "is owned by user 1234, not root or the operator"},
		"writable input":        {func(th *testHost) { th.fs.get("/srv/inputs").mode |= 0o002 }, "/srv/inputs is writable by others"},
		"missing input":         {func(th *testHost) { th.fs.remove(testInput) }, "services.web.inputs.assets"},
		"runtime link escape": {func(th *testHost) {
			th.fs.dir("/home/alice/rt", 0o755, aliceUID, aliceUID)
			th.fs.link("/home/alice/rt/lib", "../../..", aliceUID, aliceUID)
			th.cfg.Runtimes["node24"] = "/home/alice/rt"
			th.writeConfig(th.cfg)
		}, "runtimes.node24: link /home/alice/rt/lib -> ../../.. points outside"},
		"build root of another owner": {func(th *testHost) { th.fs.dir(BuildDir, 0o755, 5, 5) }, "/var/lib/lcm-build is not owned by root"},
		"ledger of another owner":     {func(th *testHost) { th.fs.dir(DeployLedger, 0o700, 5, 5) }, "/var/lib/lcm-deploy is not owned by root"},
		"backups of another owner":    {func(th *testHost) { th.fs.dir("/var/backups/lcm-services", 0o700, 5, 5) }, "/var/backups/lcm-services is not owned by root"},
		"backups of another mode":     {func(th *testHost) { th.fs.dir("/var/backups/lcm-services", 0o755, 0, 0) }, "backup.dir /var/backups/lcm-services exists with mode 0755; apply does not change"},
	} {
		t.Run(name, func(t *testing.T) {
			th := deployHost(t)
			tc.setup(th)
			th.fs.resetOps()
			_, out, err := th.apply(Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if w := th.fs.writes(); len(w) > 0 {
				t.Fatalf("wrote %v", w)
			}
		})
	}
}
