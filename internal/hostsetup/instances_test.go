package hostsetup

import (
	"reflect"
	"strings"
	"testing"
)

func TestInstanceGrantConfig(t *testing.T) {
	base := `"operator":"alice","repository":"https://github.com/ACaccel/LCM.git",` +
		`"toolchains":{"go":"/g","node":"/n"},"tailscale":{"port":9443},"components":{"deploy":true}`
	c, err := ParseConfig([]byte(`{` + base + `,
		"backup":{"dir":"/var/backups/lcm-services","tools":{"mongodump":"/opt/mongo/bin/mongodump"}},
		"services":{"bots":{"instances":{"alpha":{"port":4100},"log-archive":{"groups":["bots-db"]}},
		"groups":["shared"],"after":["mongodb-bots.service"],"wants":["mongodb-bots.service"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := c.Services["bots"]
	if !g.MultiInstance() || g.Instances["alpha"].Port != 4100 || !reflect.DeepEqual(g.GrantedGroups("log-archive"), []string{"shared", "bots-db"}) ||
		!reflect.DeepEqual(g.GrantedGroups(""), []string{"shared"}) || c.Backup.Tools["mongodump"] != "/opt/mongo/bin/mongodump" {
		t.Fatalf("parsed %+v", c)
	}
	if again, err := ParseConfig(c.Encode()); err != nil || !reflect.DeepEqual(again, c) {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	svc := func(s string) string { return `"services":{` + s + `}` }
	for in, want := range map[string]string{
		svc(`"p":{"instances":{"a":{"account":"x"}}}`):                                                               `"services.p.instances.a.account" is reserved`,
		svc(`"p":{"instances":{"a":{"credentials":{}}}}`):                                                            `"services.p.instances.a.credentials" is reserved`,
		svc(`"p":{"instances":{"a":{"runtimeDir":true}}}`):                                                           `"services.p.instances.a.runtimeDir" is reserved`,
		svc(`"p":{"instances":{"a":{"module":"x"}}}`):                                                                `"services.p.instances.a.module" is reserved`,
		svc(`"p":{"instances":{"a":{"start":"x"}}}`):                                                                 `unknown key "services.p.instances.a.start"`,
		svc(`"p":{"port":4000,"instances":{"a":{}}}`):                                                                "services.p.port must not be set with instances",
		svc(`"p":{"instances":{}}`):                                                                                  "must name at least one instance",
		svc(`"p":{"instances":{"a--b":{}}}`):                                                                         `"a--b" is not an instance name`,
		svc(`"p":{"instances":{"a-":{}}}`):                                                                           `"a-" is not an instance name`,
		svc(`"p":{"instances":{"backup":{}}}`):                                                                       "not backup",
		svc(`"p":{"instances":{"abcdefghijklmnopqrstuvwxy":{}}}`):                                                    "is not an instance name",
		svc(`"p":{"instances":{"a":{"port":4000},"b":{"port":4000}}}`):                                               "services.p.instances.b.port 4000 is also the port of services.p.instances.a",
		svc(`"a":{"port":4000},"p":{"instances":{"x":{"port":4000}}}`):                                               "services.p.instances.x.port 4000 is also the port of services.a",
		svc(`"p":{"instances":{"a":{"port":7800}}}`):                                                                 "services.p.instances.a.port 7800 is LCM's own",
		svc(`"p":{"instances":{"a":{"port":80}}}`):                                                                   "between 1024 and 65535",
		svc(`"p":{"instances":{"a":{}},"route":{"prefix":"/p/"}}`):                                                   "its route can only serve route.static /opt/p/www",
		svc(`"p":{"instances":{"a":{}},"groups":["sudo"]}`):                                                          "the group sudo is never granted",
		svc(`"p":{"instances":{"a":{"groups":["docker"]}}}`):                                                         "services.p.instances.a.groups: the group docker is never granted",
		svc(`"p":{"port":4000,"groups":["Bad"]}`):                                                                    `"Bad" is not a group name`,
		svc(`"p":{"port":4000,"groups":["p"]}`):                                                                      "already the service's own group",
		svc(`"p":{"port":4000,"groups":["a","a"]}`):                                                                  "listed twice",
		svc(`"p":{"port":4000,"groups":["a","b","c","d","e","f","g","h","i"]}`):                                      "at most 8 groups",
		svc(`"p":{"port":4000,"after":["db"]}`):                                                                      `"db" is not a unit name`,
		svc(`"p":{"port":4000,"wants":["x.timer"]}`):                                                                 `"x.timer" is not a unit name`,
		svc(`"p":{"port":4000,"wants":["p.service"]}`):                                                               "is a unit of this service or of LCM itself",
		svc(`"p":{"port":4000,"after":["p@a.service"]}`):                                                             "is a unit of this service or of LCM itself",
		svc(`"p":{"port":4000,"wants":["lcm.service"]}`):                                                             "is a unit of this service or of LCM itself",
		svc(`"p":{"port":4000,"wants":["lcm-self-install.service"]}`):                                                "is a unit of this service or of LCM itself",
		svc(`"p":{"port":4000,"wants":["reboot.target"]}`):                                                           "reboot.target is a target; only the synchronization points",
		svc(`"p":{"port":4000,"after":["suspend.target"]}`):                                                          "suspend.target is a target",
		svc(`"p":{"port":4000,"wants":["multi-user.target"]}`):                                                       "multi-user.target is a target",
		svc(`"p":{"external":true,"route":{"prefix":"/p/","static":"/srv/p"},"instances":{"a":{}}}`):                 "an external service runs no instances",
		svc(`"p":{"external":true,"route":{"prefix":"/p/","static":"/srv/p"},"wants":["db.service"]}`):               "takes no groups, after or wants",
		`"backup":{"dir":"/var/backups/s","tools":{"mysqldump":"/usr/bin/mysqldump"}}`:                               `"mysqldump" is not a database tool`,
		`"backup":{"dir":"/var/backups/s","tools":{"mongodump":"/opt/m/bin/mongo-dump"}}`:                            "must name a file called mongodump",
		`"backup":{"dir":"/var/backups/s","tools":{"mongodump":"bin/mongodump"}}`:                                    "backup.tools.mongodump must be an absolute",
		`"backup":{"dir":"/var/backups/s","tools":{"mongodump":"/var/lib/lcm/mongodump"}}`:                           "must not be inside /var/lib/lcm",
		`"backup":{"dir":"/var/backups/s","tools":{"mongodump":"/var/backups/s/mongodump"}}`:                         "must not be inside /var/backups/s",
		`"backup":{"dir":"/var/backups/s","tools":{"mongodump":"/opt/web/mongodump"}},` + svc(`"web":{"port":4000}`): "must not be inside /opt",
	} {
		if _, err := ParseConfig([]byte(`{` + base + `,` + in + `}`)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestUnitConflictDropIns(t *testing.T) {
	th := newTestHost(t)
	for _, d := range []string{"/usr/lib", "/usr/lib/systemd", "/usr/lib/systemd/system", "/etc/systemd/system"} {
		th.fs.dir(d, 0o755, 0, 0)
	}
	marked := ServiceUnitMarker + "\n[Unit]\n"
	for name, tc := range map[string]struct {
		setup func()
		check func() (string, error)
		want  string
	}{
		"dash prefix of a single unit": {func() { th.fs.dir("/etc/systemd/system/web-.service.d", 0o755, 0, 0) },
			func() (string, error) { return UnitConflict(th.fs, "web-app", 0) }, "web-.service.d exists"},
		"template": {func() { th.fs.file("/usr/lib/systemd/system/bots@.service", "x", 0o644, 0, 0) },
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"alpha"}, 0) }, "/usr/lib/systemd/system/bots@.service exists; a template"},
		"template drop-in": {func() {
			th.fs.dir("/run/systemd/system", 0o755, 0, 0)
			th.fs.dir("/run/systemd/system/bots@.service.d", 0o755, 0, 0)
		},
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"alpha"}, 0) }, "bots@.service.d exists"},
		"instance drop-in": {func() { th.fs.dir("/etc/systemd/system/bots@alpha.service.d", 0o755, 0, 0) },
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"alpha"}, 0) }, "bots@alpha.service.d exists"},
		"instance dash prefix": {func() { th.fs.dir("/etc/systemd/system/bots@log-.service.d", 0o755, 0, 0) },
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"log-archive"}, 0) }, "bots@log-.service.d exists"},
		"unmarked instance unit": {func() { th.fs.file("/etc/systemd/system/bots@alpha.service", "[Unit]\n", 0o644, 0, 0) },
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"alpha"}, 0) }, "was not written by the deploy helper"},
		"marked instance unit": {func() { th.fs.file("/etc/systemd/system/bots@alpha.service", marked, 0o644, 0, 0) },
			func() (string, error) { return InstanceConflicts(th.fs, "bots", []string{"alpha"}, 0) }, ""},
	} {
		t.Run(name, func(t *testing.T) {
			th = newTestHost(t)
			for _, d := range []string{"/usr/lib", "/usr/lib/systemd", "/usr/lib/systemd/system", "/run", "/run/systemd"} {
				th.fs.dir(d, 0o755, 0, 0)
			}
			tc.setup()
			got, err := tc.check()
			if err != nil || (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("conflict = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGroupProblem(t *testing.T) {
	primary := map[int]string{990: "lcm", 977: SecurityAccount}
	for g, want := range map[Group]string{
		{"bots-db", 982}:              "",
		{"sudo", 27}:                  "never granted",
		{"systemd-journal", 999}:      "never granted",
		{"floppy", 25}:                "static system group",
		{"wheel2", 0}:                 "static system group",
		{"alice", 1000}:               "not a system group",
		{"nogroup", 65534}:            "not a system group",
		{"lcmshared", 990}:            "primary group of the account lcm",
		{"users", 100}:                "never granted",
		{"users2", 100}:               "static system group",
		{"ssl-cert", 111}:             "never granted",
		{"workstation-security", 976}: "never granted",
		{"collector", 977}:            "primary group of the account workstation-security",
	} {
		got := GroupProblem(g, 1000, primary)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%+v: %q, want %q", g, got, want)
		}
	}
}

// botsGrant is a grant with every multi-instance key.
func botsGrant() ServiceGrant {
	return ServiceGrant{
		Runtimes: []string{"node24"},
		Instances: map[string]InstanceGrant{
			"alpha": {Port: 4100}, "log-archive": {Groups: []string{"bots-db"}},
		},
		After: []string{"mongodb-bots.service"}, Wants: []string{"mongodb-bots.service"},
	}
}

func TestAllowInstances(t *testing.T) {
	th := deployHost(t)
	th.converge()
	th.r.groups = map[string]Group{"bots-db": {Name: "bots-db", GID: 982}}
	th.fs.resetOps()
	res, out, err := th.allow(AllowOptions{Name: "bots", Grant: botsGrant()})
	if err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	acc := th.r.accounts["bots"]
	for _, p := range []string{"/etc/bots/env", "/etc/bots/alpha.env", "/etc/bots/log-archive.env"} {
		if n := th.fs.get(p); n == nil || n.mode != 0o640 || n.uid != 0 || n.gid != acc.GID || len(n.data) != 0 {
			t.Errorf("%s = %+v", p, n)
		}
		if !strings.Contains(out, "create an empty "+p+" (root:bots 0640)") {
			t.Errorf("output lacks %s:\n%s", p, out)
		}
	}
	cfg, err := LoadConfig(th.fs, ConfigPath)
	if err != nil || !reflect.DeepEqual(cfg.Services["bots"], botsGrant()) {
		t.Fatalf("host.json: %+v %v", cfg.Services["bots"], err)
	}

	// An existing instance env file is kept; a removed instance's file and
	// unit are reported, not removed.
	th.fs.file("/etc/bots/alpha.env", "TOKEN=x\n", 0o600, 0, 0)
	th.fs.file(InstanceUnit("bots", "log-archive"), ServiceUnitMarker+"\n[Unit]\n", 0o644, 0, 0)
	g := botsGrant()
	delete(g.Instances, "log-archive")
	res, out, err = th.allow(AllowOptions{Name: "bots", Grant: g})
	if err != nil || res.Manual != 0 {
		t.Fatalf("allow again: %+v %v\n%s", res, err, out)
	}
	// root:root 0600 is stricter than what apply creates and is kept.
	if n := th.fs.get("/etc/bots/alpha.env"); string(n.data) != "TOKEN=x\n" || n.mode != 0o600 || n.gid != 0 {
		t.Errorf("kept env file %+v", n)
	}
	for _, want := range []string{
		"/etc/bots/log-archive.env belongs to the instance log-archive, which is no longer granted; it is kept",
		"/etc/systemd/system/bots@log-archive.service belongs to the instance log-archive, which is no longer granted; it keeps running until the next deployment or stop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if th.fs.get("/etc/bots/log-archive.env") == nil {
		t.Error("removed the env file of a removed instance")
	}
	th.expectConverged()

	// An instance env file others can read is not changed silently.
	th.fs.file("/etc/bots/alpha.env", "TOKEN=x\n", 0o644, 0, 0)
	res, out, err = th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "/etc/bots/alpha.env is 0:0 0644; review its content") {
		t.Fatalf("apply: %+v %v\n%s", res, err, out)
	}
	if n := th.fs.get("/etc/bots/alpha.env"); n.mode != 0o644 {
		t.Errorf("changed the env file: %+v", n)
	}
}

// A single-instance service's own *.env files are not instances.
func TestSingleServiceEnvFilesAreNotInstances(t *testing.T) {
	th := deployHost(t)
	th.converge()
	th.fs.file("/etc/web/extra.env", "A=1\n", 0o640, 0, th.r.accounts["web"].GID)
	_, out, err := th.apply(Options{})
	if err != nil || strings.Contains(out, "extra.env") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
}

func TestAllowInstanceRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(th *testHost)
		grant ServiceGrant
		want  string
	}{
		"missing group": {nil, botsGrant(), "the group bots-db of services.bots does not exist"},
		"primary group of LCM": {func(th *testHost) {
			th.r.groups["bots-db"] = Group{Name: "bots-db", GID: th.r.accounts["lcm"].GID}
		}, botsGrant(), "primary group of the account lcm"},
		"primary group of another service": {func(th *testHost) {
			th.converge()
			th.r.groups["bots-db"] = Group{Name: "bots-db", GID: th.r.accounts["web"].GID}
		}, botsGrant(), "primary group of the account web"},
		"login group": {func(th *testHost) {
			th.r.groups["bots-db"] = Group{Name: "bots-db", GID: 1000}
		}, botsGrant(), "is not a system group"},
		"unmarked instance unit": {func(th *testHost) {
			th.r.groups["bots-db"] = Group{Name: "bots-db", GID: 982}
			th.fs.file(InstanceUnit("bots", "alpha"), "[Unit]\n", 0o644, 0, 0)
		}, botsGrant(), "bots@alpha.service exists and was not written by the deploy helper"},
		"outdated pinned helper": {func(th *testHost) {
			th.converge()
			th.fs.file(DeployHelper, "older helper", 0o755, 0, 0)
		}, botsGrant(), "run sudo bin/lcm host apply first"},
	} {
		t.Run(name, func(t *testing.T) {
			th := deployHost(t)
			th.r.groups = map[string]Group{}
			if tc.setup != nil {
				tc.setup(th)
			}
			before := th.fs.content(ConfigPath)
			_, out, err := th.allow(AllowOptions{Name: "bots", Grant: tc.grant})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if th.fs.content(ConfigPath) != before {
				t.Fatal("host.json changed")
			}
		})
	}

	// A plain grant is allowed with an outdated helper: it uses no new key.
	th := deployHost(t)
	th.converge()
	th.fs.file(DeployHelper, "older helper", 0o755, 0, 0)
	if _, out, err := th.allow(AllowOptions{Name: "other", Grant: ServiceGrant{Port: 3300}}); err != nil {
		t.Fatalf("plain grant: %v\n%s", err, out)
	}
}

func TestBackupToolsApply(t *testing.T) {
	th := deployHost(t)
	th.fs.dir("/opt/mongo", 0o755, 0, 0)
	th.fs.dir("/opt/mongo/bin", 0o755, 0, 0)
	th.fs.file("/opt/mongo/bin/mongodump", "x", 0o750, 0, 0)
	th.fs.file("/opt/mongo/bin/mongorestore", "x", 0o755, aliceUID, 0)
	th.cfg.Backup.Tools = map[string]string{"mongodump": "/opt/mongo/bin/mongodump", "mongorestore": "/opt/mongo/bin/mongorestore"}
	th.writeConfig(th.cfg)
	res, out, err := th.apply(Options{})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if res.Manual != 1 || !strings.Contains(out, "backup.tools.mongorestore: /opt/mongo/bin/mongorestore is not root's alone") ||
		!strings.Contains(out, "backup.tools.mongodump: /opt/mongo/bin/mongodump is not executable by others") {
		t.Fatalf("apply: %+v\n%s", res, out)
	}
	if _, err := TrustedTool(th.fs, "/opt/mongo/bin/mongodump", 0); err != nil {
		t.Fatal(err)
	}
}
