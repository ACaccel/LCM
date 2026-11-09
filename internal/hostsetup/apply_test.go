package hostsetup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestFreshInstall(t *testing.T) {
	th := newTestHost(t)

	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Changes == 0 || res.Manual != 0 {
		t.Fatalf("check on a fresh host: %+v %v\n%s", res, err, out)
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("check wrote %v", w)
	}
	for _, want := range []string{"account:         would change: create the system account lcm",
		"directories:     would change: create /opt (root:root 0755)", "would change: install /etc/sudoers.d/lcm",
		"would change: clone https://github.com/ACaccel/LCM.git", "would change: start lcm.service",
		"would change: serve https://host.example.ts.net:9443"} {
		if !strings.Contains(out, want) {
			t.Errorf("check output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "create /opt/lcm ") != 1 {
		t.Errorf("check reports a planned directory twice:\n%s", out)
	}
	if n := len(th.r.called("useradd")) + len(th.r.called("systemctl start")) + len(th.r.called("[lcm] git clone")); n != 0 {
		t.Fatalf("check ran changing commands: %v", th.r.calls)
	}

	th.r.resetCalls()
	res, out, err = th.apply(Options{Password: func() (string, error) { return "correct horse battery", nil }})
	if err != nil || res.Changes == 0 {
		t.Fatalf("apply: %+v %v\n%s", res, err, out)
	}
	if th.r.passwordInput != "correct horse battery\n" {
		t.Errorf("set-password read %q from its pipe", th.r.passwordInput)
	}
	if strings.Contains(out, "correct horse") || strings.Contains(out, `User "admin"`) {
		t.Errorf("the password or child output reached the terminal:\n%s", out)
	}
	if !strings.Contains(out, "LCM is running at https://host.example.ts.net:9443") {
		t.Errorf("no final message:\n%s", out)
	}

	// The documented order: the account first, grants after the files they
	// grant, the service after its unit and checkout, the token after
	// health, tailscale serve last.
	order := []string{"useradd", "[lcm] /opt/lcm/toolchain/go.new/bin/go version", "visudo -c", "systemd-analyze verify",
		"[lcm] git clone", "systemctl daemon-reload", "systemctl enable", "systemctl start",
		"[lcm] /usr/local/bin/lcm admin set-password", "[lcm] /usr/local/bin/lcm token create", "[alice] sh -c", "tailscale serve --bg"}
	last := -1
	for _, c := range order {
		i := th.r.index(c)
		if i <= last {
			t.Fatalf("%q at %d, not after %d: %v", c, i, last, th.r.calls)
		}
		last = i
	}

	lcm := th.r.accounts["lcm"]
	for _, f := range []struct {
		path     string
		perm     fs.FileMode
		uid, gid int
		data     string
	}{
		{EnvFile, 0o640, 0, lcm.GID, string(asset("lcm.env"))},
		{Binary, 0o755, 0, 0, exeData},
		{SelfInstall, 0o755, 0, 0, string(asset("self-install"))},
		{Sudoers, 0o440, 0, 0, string(asset("sudoers"))},
		{Unit, 0o644, 0, 0, string(asset("lcm.service"))},
		{ToolchainDir + "/go/bin/go", 0o755, 0, 0, "go binary"},
		{ToolchainDir + "/go/src/fmt/print.go", 0o644, 0, 0, "package fmt"},
		{"/home/alice/.config/lcm/token", 0o600, aliceUID, aliceUID, testToken + "\n"},
	} {
		n := th.fs.get(f.path)
		if n == nil || n.mode != f.perm || n.uid != f.uid || n.gid != f.gid || string(n.data) != f.data {
			t.Errorf("%s = %+v, want %04o %d:%d %q", f.path, n, f.perm, f.uid, f.gid, f.data)
		}
	}
	for _, d := range []struct {
		path     string
		perm     fs.FileMode
		uid, gid int
	}{
		{DataDir, 0o750, lcm.UID, lcm.GID}, {WorkDir, 0o750, lcm.UID, lcm.GID}, {CacheDir, 0o750, lcm.UID, lcm.GID},
		{EtcDir, 0o755, 0, 0}, {"/opt", 0o755, 0, 0}, {ToolchainDir, 0o755, 0, 0}, {LibexecDir, 0o755, 0, 0},
		{ToolchainDir + "/node", 0o755, 0, 0},
	} {
		n := th.fs.get(d.path)
		if n == nil || n.mode != fs.ModeDir|d.perm || n.uid != d.uid || n.gid != d.gid {
			t.Errorf("%s = %+v, want dir %04o %d:%d", d.path, n, d.perm, d.uid, d.gid)
		}
	}
	if n := th.fs.get(ToolchainDir + "/node/bin/npm"); n == nil || n.target != "../lib/node_modules/npm/bin/npm-cli.js" || n.uid != 0 {
		t.Errorf("npm link = %+v", n)
	}
	if n := th.fs.get(ToolchainDir + "/go.new"); n != nil {
		t.Errorf("staging copy left behind")
	}
	for p := range th.fs.nodes {
		if strings.HasPrefix(p, "/tmp/") {
			t.Errorf("staging file left behind: %s", p)
		}
	}
	if strings.Contains(out, testToken) {
		t.Errorf("the token appears in the output:\n%s", out)
	}

	// A database exists now, so a rerun does not ask for a password.
	th.fs.file(DatabaseFile, "db", 0o600, lcm.UID, lcm.GID)
	th.expectConverged()
}

func TestFreshInstallWithoutTerminal(t *testing.T) {
	th := newTestHost(t)
	_, out, err := th.apply(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(th.r.called("[lcm] /usr/local/bin/lcm admin")) != 0 || !strings.Contains(out, "not a terminal; set it with: sudo -u lcm") {
		t.Errorf("admin password without a terminal:\n%s", out)
	}
}

// converged returns a test host on which apply has run.
func converged(t *testing.T) *testHost {
	t.Helper()
	th := newTestHost(t)
	th.converge()
	lcm := th.r.accounts["lcm"]
	th.fs.file(DatabaseFile, "db", 0o600, lcm.UID, lcm.GID)
	th.expectConverged()
	th.r.resetCalls()
	return th
}

func TestDriftIsDetectedAndFixed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift func(th *testHost)
		want  string
		calls []string
	}{
		{"self-install content", func(th *testHost) { th.fs.file(SelfInstall, "#!/bin/sh\nexit 0\n", 0o755, 0, 0) },
			"self-install:    would change: update /usr/local/libexec/lcm/self-install", nil},
		{"self-install link", func(th *testHost) { th.fs.remove(SelfInstall); th.fs.link(SelfInstall, "/tmp/x", 0, 0) },
			"would change: replace /usr/local/libexec/lcm/self-install (not a regular file)", nil},
		{"self-install owner", func(th *testHost) {
			th.fs.file(SelfInstall, string(asset("self-install")), 0o755, aliceUID, aliceUID)
		}, "would change: replace /usr/local/libexec/lcm/self-install (owned by 1000:1000)", nil},
		{"sudoers mode", func(th *testHost) { th.fs.get(Sudoers).mode = 0o644 },
			"sudoers:         would change: set the mode of /etc/sudoers.d/lcm to 0440 (was 0644)", []string{"visudo -c"}},
		{"sudoers missing", func(th *testHost) { th.fs.remove(Sudoers) },
			"would change: install /etc/sudoers.d/lcm", []string{"visudo -c"}},
		{"unit content", func(th *testHost) { th.fs.file(Unit, "[Unit]\n", 0o644, 0, 0) },
			"unit:            would change: update /etc/systemd/system/lcm.service",
			[]string{"systemd-analyze verify", "systemctl daemon-reload", "systemctl restart"}},
		{"unit needs reload", func(th *testHost) { th.r.needReload = true },
			"service:         would change: reload the systemd configuration", []string{"systemctl daemon-reload"}},
		{"binary missing", func(th *testHost) { th.fs.remove(Binary) },
			"binary:          would change: install /usr/local/bin/lcm", nil},
		{"lcm.env mode", func(th *testHost) { th.fs.get(EnvFile).mode = 0o644 },
			"lcm.env:         would change: set /etc/lcm/lcm.env to root:lcm 0640", nil},
		{"data directory owner", func(th *testHost) { n := th.fs.get(WorkDir); n.uid, n.gid = 0, 0 },
			"directories:     would change: set /var/lib/lcm/work to lcm:lcm 0750", nil},
		{"root directory mode", func(th *testHost) { th.fs.get(OptDir).mode = fs.ModeDir | 0o700 },
			"would change: set the mode of /opt/lcm to 0755 (was 0700)", nil},
		{"toolchain copy edited", func(th *testHost) { th.fs.file(ToolchainDir+"/go/bin/go", "evil", 0o755, 0, 0) },
			"toolchain go:    would change: copy /home/alice/.local/go to /opt/lcm/toolchain/go (bin/go differs)",
			[]string{"[lcm] /opt/lcm/toolchain/go.new/bin/go version"}},
		{"toolchain copy writable", func(th *testHost) { th.fs.get(ToolchainDir + "/go/bin/go").mode = 0o777 },
			"(bin/go has another mode)", nil},
		{"toolchain source updated", func(th *testHost) {
			th.fs.file("/home/alice/.local/node/lib/new.js", "x", 0o644, aliceUID, aliceUID)
		}, "toolchain node:  would change: copy /home/alice/.local/node to /opt/lcm/toolchain/node (entries of lib differ)",
			[]string{"[lcm] /opt/lcm/toolchain/node.new/bin/node --version"}},
		{"checkout missing", func(th *testHost) { th.fs.remove(Checkout) },
			"checkout:        would change: clone", []string{"[lcm] git clone"}},
		{"service disabled", func(th *testHost) { th.r.enabled = false },
			"service:         would change: enable lcm.service", []string{"systemctl enable"}},
		{"service stopped", func(th *testHost) { th.r.active, th.r.healthy = false, false },
			"service:         would change: start lcm.service", []string{"systemctl start"}},
		{"token removed", func(th *testHost) { th.fs.remove("/home/alice/.config/lcm/token") },
			"token:           would change: write an admin API token to /home/alice/.config/lcm/token",
			[]string{"[lcm] /usr/local/bin/lcm token create", "[alice] sh -c"}},
		{"serve removed", func(th *testHost) { th.r.serve = "" },
			"tailscale serve: would change: serve https://host.example.ts.net:9443", []string{"tailscale serve --bg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := converged(t)
			tc.drift(th)
			th.fs.resetOps()
			res, out, err := th.apply(Options{Check: true})
			if err != nil || res.Changes == 0 || !strings.Contains(out, tc.want) {
				t.Fatalf("check: %+v %v, want %q in\n%s", res, err, tc.want, out)
			}
			if w := th.fs.writes(); len(w) > 0 {
				t.Fatalf("check wrote %v", w)
			}
			th.r.resetCalls()
			res, out, err = th.apply(Options{})
			if err != nil || res.Changes == 0 || !strings.Contains(out, strings.Replace(tc.want, "would change", "changed", 1)) {
				t.Fatalf("apply: %+v %v\n%s", res, err, out)
			}
			for _, c := range tc.calls {
				if len(th.r.called(c)) == 0 {
					t.Errorf("apply did not run %q: %v", c, th.r.calls)
				}
			}
			th.expectConverged()
		})
	}
}

func TestKeptFilesAreNotReplaced(t *testing.T) {
	th := converged(t)
	th.fs.file(Binary, "self-deployed build", 0o755, 0, 0)
	th.fs.file(EnvFile, "LCM_LOG_LEVEL=debug\n", 0o640, 0, th.r.accounts["lcm"].GID)
	res, out, err := th.apply(Options{})
	if err != nil || res.Changes != 0 {
		t.Fatalf("%+v %v\n%s", res, err, out)
	}
	for _, want := range []string{"kept the installed binary", "kept the existing file"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q note:\n%s", want, out)
		}
	}
	if th.fs.content(Binary) != "self-deployed build" || th.fs.content(EnvFile) != "LCM_LOG_LEVEL=debug\n" {
		t.Fatal("apply replaced a kept file")
	}
	if _, out, err := th.apply(Options{ReplaceBinary: true}); err != nil || !strings.Contains(out, "changed: update /usr/local/bin/lcm") {
		t.Fatalf("--replace-binary: %v\n%s", err, out)
	}
	if th.fs.content(Binary) != exeData {
		t.Fatal("--replace-binary did not install the running executable")
	}
}

func TestRefusedBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(th *testHost)
		want  string
	}{
		{"not root", func(th *testHost) { th.host.Privileged = false }, "must run as root"},
		{"self-deployed executable", func(th *testHost) {
			th.fs.file(Binary, exeData, 0o755, 0, 0)
			th.host.Executable = Binary
		}, "refusing to run /usr/local/bin/lcm as root"},
		{"no config", func(th *testHost) { th.fs.remove(ConfigPath) }, "/etc/lcm/host.json does not exist"},
		{"config not JSON", func(th *testHost) { th.fs.file(ConfigPath, "operator: alice", 0o644, 0, 0) }, "invalid character"},
		{"unknown key", func(th *testHost) { th.patchConfig(`"operator"`, `"hostname":"x","operator"`) }, `unknown key "hostname"`},
		{"other case", func(th *testHost) { th.patchConfig(`"operator"`, `"Operator"`) }, `unknown key "Operator"`},
		{"duplicate key", func(th *testHost) { th.patchConfig(`"operator": "alice"`, `"operator":"root","operator":"alice"`) }, `duplicate key "operator"`},
		{"nested unknown key", func(th *testHost) { th.patchConfig(`"port": 9443`, `"port":9443,"host":"x"`) }, `unknown key "tailscale.host"`},
		{"trailing data", func(th *testHost) { th.fs.file(ConfigPath, string(th.cfg.Encode())+"{}", 0o644, 0, 0) }, "unexpected data after"},
		{"root operator", func(th *testHost) { th.cfg.Operator = "root"; th.writeConfig(th.cfg) }, "operator must name a non-root account"},
		{"http repository", func(th *testHost) { th.cfg.Repository = "http://example.com/x.git"; th.writeConfig(th.cfg) }, "repository must be an https URL"},
		{"credentials in repository", func(th *testHost) { th.cfg.Repository = "https://u:p@example.com/x.git"; th.writeConfig(th.cfg) }, "repository must be an https URL"},
		{"relative toolchain", func(th *testHost) { th.cfg.Toolchains.Go = "go"; th.writeConfig(th.cfg) }, "toolchains.go must be an absolute"},
		{"port", func(th *testHost) { th.cfg.Tailscale.Port = 0; th.writeConfig(th.cfg) }, "tailscale.port must be between"},
		{"machine id", func(th *testHost) {
			th.cfg.Components.SecurityHost = &SecurityHost{MachineID: "../x"}
			th.writeConfig(th.cfg)
		}, "machineId must be"},
		{"config writable", func(th *testHost) { th.fs.get(ConfigPath).mode = 0o664 }, "host.json is writable by group or others"},
		{"config owner", func(th *testHost) { th.fs.get(ConfigPath).uid = aliceUID }, "host.json is not owned by root"},
		{"config link", func(th *testHost) {
			th.fs.file("/etc/lcm/real.json", string(th.cfg.Encode()), 0o644, 0, 0)
			th.fs.remove(ConfigPath)
			th.fs.link(ConfigPath, "real.json", 0, 0)
		}, "host.json is a symbolic link"},
		{"config directory writable", func(th *testHost) { th.fs.get(EtcDir).mode = fs.ModeDir | 0o777 }, "/etc/lcm is writable by group or others"},
		{"missing tool", func(th *testHost) { th.r.missingTools["visudo"] = true }, "missing required tools: visudo"},
		{"untrusted directory", func(th *testHost) { th.fs.get("/usr/local/bin").uid = aliceUID }, "/usr/local/bin is not owned by root"},
		{"libexec not root", func(th *testHost) { th.fs.dir(LibexecDir, 0o755, aliceUID, aliceUID) }, "/usr/local/libexec/lcm is not owned by root"},
		{"toolchain directory writable", func(th *testHost) { th.fs.dir(ToolchainDir, 0o775, 0, 0) }, "/opt/lcm/toolchain is writable by group or others"},
		{"libexec writable", func(th *testHost) { th.fs.dir(LibexecDir, 0o757, 0, 0) }, "/usr/local/libexec/lcm is writable by group or others"},
		{"data directory is a link", func(th *testHost) { th.fs.link(DataDir, "/home/alice", 0, 0) }, "/var/lib/lcm exists but is not a directory"},
		{"unit is a directory", func(th *testHost) { th.fs.dir(Unit, 0o755, 0, 0) }, "lcm.service exists but is not a regular file"},
		{"no operator", func(th *testHost) { delete(th.r.accounts, "alice") }, "no such non-root account: alice"},
		{"lcm with another home", func(th *testHost) {
			th.r.accounts["lcm"] = Account{Name: "lcm", UID: 990, GID: 990, Home: "/home/lcm"}
		}, "account lcm exists with home directory /home/lcm"},
		{"no go", func(th *testHost) { th.fs.remove("/home/alice/.local/go/bin/go") }, "toolchains.go: no executable bin/go"},
		{"go not executable", func(th *testHost) { th.fs.get("/home/alice/.local/go/bin/go").mode = 0o644 }, "toolchains.go: no executable bin/go"},
		{"no npm", func(th *testHost) { th.fs.remove("/home/alice/.local/node/bin/npm") }, "toolchains.node: no bin/npm"},
		{"toolchain is a link", func(th *testHost) {
			th.fs.remove("/home/alice/.local/go")
			th.fs.link("/home/alice/.local/go", "go1.26", aliceUID, aliceUID)
		}, "toolchains.go: /home/alice/.local/go is a symbolic link"},
		{"toolchain in a directory of lcm", func(th *testHost) {
			th.fs.get("/home/alice/.local").uid = 990
		}, "toolchains.go: /home/alice/.local is owned by user 990, not root or the operator"},
		{"toolchain writable by others", func(th *testHost) {
			th.fs.get("/home/alice/.local/node").mode = fs.ModeDir | 0o777
		}, "toolchains.node: /home/alice/.local/node is writable by others"},
		{"toolchain writable by another group", func(th *testHost) {
			th.fs.get("/home/alice/.local/go").gid = 990
			th.fs.get("/home/alice/.local/go").mode = fs.ModeDir | 0o775
		}, "toolchains.go: /home/alice/.local/go is writable by group 990"},
		{"not signed in", func(th *testHost) { th.r.dns = "" }, "tailscale reports no tailnet name"},
		{"port taken", func(th *testHost) {
			th.r.serve = `{"TCP":{"9443":{"HTTPS":true}},"Web":{"host.example.ts.net:9443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`
		}, "tailscale serve already uses port 9443"},
		{"port funneled", func(th *testHost) {
			th.r.serve = strings.TrimSuffix(ourServe, "}") + `,"AllowFunnel":{"host.example.ts.net:9443":true}}`
		}, "tailscale serve already uses port 9443"},
		{"not an lcm binary", func(th *testHost) { th.r.versionOutput = "something else\n" }, "does not look like an lcm binary"},
		{"sudoers rule rejected", func(th *testHost) { th.r.failVisudoStaged = true }, "the embedded sudoers rule does not pass visudo"},
		{"checkout of another repository", func(th *testHost) {
			th.r.accounts["lcm"] = Account{Name: "lcm", UID: 990, GID: 990, Home: DataDir}
			th.fs.dir(Checkout+"/.git", 0o755, 990, 990)
			th.r.origin = "https://example.com/fork.git"
		}, "tracks https://example.com/fork.git, not the configured repository"},
		{"checkout without git", func(th *testHost) { th.fs.dir(Checkout, 0o750, 990, 990) }, "is not a git checkout"},
		{"no python for the security host", func(th *testHost) {
			th.fs.remove(python3)
			th.cfg.Components.SecurityHost = &SecurityHost{MachineID: "m-1"}
			th.writeConfig(th.cfg)
		}, "the security host installer needs /usr/bin/python3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := newTestHost(t)
			tc.setup(th)
			th.fs.resetOps()
			for _, check := range []bool{true, false} {
				res, out, err := th.apply(Options{Check: check})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("check=%v: err = %v, want %q\n%s", check, err, tc.want, out)
				}
				if res.Changes != 0 || out != "" {
					t.Fatalf("check=%v reported steps before failing: %+v\n%s", check, res, out)
				}
			}
			if w := th.fs.writes(); len(w) > 0 {
				t.Fatalf("wrote %v", w)
			}
			for _, c := range []string{"useradd", "systemctl", "[lcm] git clone", "[lcm] /usr/local/bin/lcm", "tailscale serve --bg"} {
				if got := th.r.called(c); len(got) > 0 {
					t.Fatalf("ran %v", got)
				}
			}
		})
	}
}

// patchConfig replaces text in the encoded configuration file.
func (th *testHost) patchConfig(old, repl string) {
	th.t.Helper()
	data := string(th.cfg.Encode())
	if !strings.Contains(data, old) {
		th.t.Fatalf("%q not in %s", old, data)
	}
	th.fs.file(ConfigPath, strings.Replace(data, old, repl, 1), 0o644, 0, 0)
}

func TestSudoersFailureIsRolledBack(t *testing.T) {
	th := newTestHost(t)
	th.r.failVisudo = true
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "sudoers no longer parses with /etc/sudoers.d/lcm; removed it") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.get(Sudoers) != nil {
		t.Fatal("the rule is still installed")
	}
	if th.fs.get(Unit) != nil || len(th.r.called("systemctl")) > 0 {
		t.Fatal("apply continued after the failed grant")
	}
}

func TestMachinePowerGrantFailureIsRolledBack(t *testing.T) {
	th := converged(t)
	th.cfg.Components.MachinePower = true
	th.writeConfig(th.cfg)
	th.r.failVisudo = true
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "sudoers no longer parses with /etc/sudoers.d/lcm-machine-power; removed it") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.get(PowerSudoers) != nil {
		t.Fatal("the machine power rule is still installed")
	}
	if th.fs.get(Sudoers) == nil {
		t.Fatal("the core rule was removed")
	}
}

func TestUnitVerifyFailureRestoresThePreviousUnit(t *testing.T) {
	th := converged(t)
	th.fs.file(Unit, "[Unit]\nDescription=old\n", 0o644, 0, 0)
	th.r.failAnalyze = true
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "does not verify; restored the previous unit") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.content(Unit) != "[Unit]\nDescription=old\n" {
		t.Fatalf("unit = %q", th.fs.content(Unit))
	}
	if len(th.r.called("systemctl restart")) > 0 {
		t.Fatal("restarted after a failed verification")
	}
}

func TestToolchainLinkEscapeIsRefused(t *testing.T) {
	for name, links := range map[string]map[string]string{
		"absolute":       {"bin/sh": "/bin/sh"},
		"parent":         {"lib/x": "../../../../etc/shadow"},
		"through a link": {"here": ".", "lib/out": "../here/.."},
		"chain":          {"a": "b", "b": "../c"},
	} {
		t.Run(name, func(t *testing.T) {
			th := newTestHost(t)
			for p, target := range links {
				th.fs.link("/home/alice/.local/go/"+p, target, aliceUID, aliceUID)
			}
			th.fs.resetOps()
			_, out, err := th.apply(Options{})
			if err == nil || !strings.Contains(err.Error(), "points outside /home/alice/.local/go") {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if w := th.fs.writes(); len(w) > 0 {
				t.Fatalf("wrote %v", w)
			}
		})
	}
}

func TestResolveInside(t *testing.T) {
	m := newMemFS()
	m.dir("/t/a/b", 0o755, 0, 0)
	for p, target := range map[string]string{
		"/t/ok":       "a/b",
		"/t/a/up":     "../a",
		"/t/dangling": "a/missing/../b",
		"/t/self":     ".",
		"/t/viaself":  "self/a",
		"/t/a/rel":    "../viaself/b",
		"/t/loop1":    "loop2",
		"/t/loop2":    "loop1",
		"/t/esc":      "self/..",
	} {
		m.link(p, target, 0, 0)
	}
	for _, ok := range []string{"ok", "a/up", "dangling", "self", "viaself", "a/rel"} {
		if err := resolveInside(fsTree{fs: m, root: "/t"}, "/t", strings.Split(ok, "/")); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"loop1", "esc"} {
		if err := resolveInside(fsTree{fs: m, root: "/t"}, "/t", strings.Split(bad, "/")); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestToolchainCopyIsCheckedAgain(t *testing.T) {
	// A link that changes between the check and the copy is caught on the
	// copy, which is then discarded.
	th := newTestHost(t)
	fsys := &swapFS{memFS: th.fs, at: "/home/alice/.local/go/misc"}
	th.host.FS = fsys
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "points outside /opt/lcm/toolchain/go.new") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.get(ToolchainDir+"/go.new") != nil || th.fs.get(ToolchainDir+"/go") != nil {
		t.Fatal("the rejected copy was kept")
	}
}

// swapFS replaces a link's target with an escaping one after it has been
// read once.
type swapFS struct {
	*memFS
	at    string
	reads int
}

func (s *swapFS) OpenTree(root string) (Tree, error) { return fsTree{fs: s, root: root}, nil }

func (s *swapFS) Readlink(p string) (string, error) {
	if p == s.at {
		if s.reads++; s.reads > 1 {
			return "/etc", nil
		}
	}
	return s.memFS.Readlink(p)
}

func TestHealthWaitIsBounded(t *testing.T) {
	th := converged(t)
	th.r.active = false
	th.host.Health = func(context.Context) error { return context.DeadlineExceeded }
	start := th.clock.Now()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for th.clock.WaitForTimers(ctx, 1) == nil {
			th.clock.Advance(time.Second)
		}
	}()
	_, _, err := th.apply(Options{})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "LCM is not healthy after 1m0s") {
		t.Fatalf("err = %v", err)
	}
	if waited := th.clock.Now().Sub(start); waited != healthWait {
		t.Errorf("waited %s", waited)
	}
}

func TestOptionalComponents(t *testing.T) {
	th := converged(t)
	th.cfg.Components.MachinePower = true
	th.cfg.Components.SecurityHost = &SecurityHost{MachineID: "m-1"}
	th.writeConfig(th.cfg)
	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Changes != 4 {
		t.Fatalf("check: %+v %v\n%s", res, err, out)
	}
	_, out, err = th.apply(Options{})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for _, f := range []struct {
		path string
		perm fs.FileMode
		data string
	}{
		{PowerWrapper, 0o755, string(asset("machine-power"))},
		{PowerHelper, 0o755, exeData},
		{PowerSudoers, 0o440, string(asset("machine-power-sudoers"))},
	} {
		if n := th.fs.get(f.path); n == nil || n.mode != f.perm || n.uid != 0 || string(n.data) != f.data {
			t.Errorf("%s = %+v", f.path, n)
		}
	}
	if !strings.Contains(out, "/etc/lcm/machine-power.json is not provisioned") {
		t.Errorf("no note about the machine power configuration:\n%s", out)
	}
	py := strings.Join(th.r.installPy, " ")
	if !strings.HasPrefix(py, "-I /tmp/lcm-security-host-") || !strings.HasSuffix(py, "/install.py --machine-id m-1 --helper-sha256 "+sha(string(asset("security-host/helper.py")))) {
		t.Errorf("install.py invocation: %s", py)
	}
	th.expectConverged()

	// An installed helper that differs needs the reviewed upgrade.
	th.fs.file(SecurityHelper, "older helper", 0o644, 0, 0)
	res, out, err = th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "security-host:   needs manual action: the installed helper.py differs") {
		t.Fatalf("outdated helper: %+v %v\n%s", res, err, out)
	}
	if th.fs.content(SecurityHelper) != "older helper" || len(th.r.called(python3)) != 1 {
		t.Fatal("apply touched an existing security host installation")
	}
	th.fs.file(SecurityHelper, string(asset("security-host/helper.py")), 0o644, 0, 0)
	th.cfg.Components.SecurityHost.MachineID = "m-2"
	th.writeConfig(th.cfg)
	if res, out, _ := th.apply(Options{Check: true}); res.Manual != 1 || !strings.Contains(out, `the installed policy is for Machine "m-1"`) {
		t.Fatalf("other machine: %+v\n%s", res, out)
	}

	// Turning components off keeps them.
	th.cfg.Components = Components{}
	th.writeConfig(th.cfg)
	th.fs.resetOps()
	res, out, err = th.apply(Options{Check: true})
	if err != nil || res.Changes != 0 || res.Manual != 0 {
		t.Fatalf("off: %+v %v\n%s", res, err, out)
	}
	for _, want := range []string{"machine-power:   unchanged (installed but disabled", "security-host:   unchanged (installed but disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q:\n%s", want, out)
		}
	}
	th.expectConverged()
	if th.fs.get(PowerSudoers) == nil || th.fs.get(SecuritySudoers) == nil {
		t.Fatal("a disabled component was removed")
	}
}

func TestSecurityHostIncompleteNeedsReview(t *testing.T) {
	th := converged(t)
	th.cfg.Components.SecurityHost = &SecurityHost{MachineID: "m-1"}
	th.writeConfig(th.cfg)
	th.fs.file(SecurityPolicy, `{"machineId":"m-1"}`, 0o600, 0, 0)
	res, out, err := th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "the installation is incomplete") || len(th.r.called(python3)) != 0 {
		t.Fatalf("%+v %v\n%s", res, err, out)
	}
}

func TestServeState(t *testing.T) {
	for in, want := range map[string]string{
		``:                                "free",
		`{}`:                              "free",
		`{"TCP":{}}`:                      "free",
		`{"TCP":{"443":{"HTTPS":true}}}`:  "free",
		ourServe:                          "ours",
		`{"TCP":{"9443":{"HTTPS":true}}}`: "taken",
		`{"TCP":{"9443":{"HTTPS":true}},"Web":{"host.example.ts.net:9443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7800"},"/x":{"Text":"x"}}}}}`: "taken",
	} {
		got, err := serveState([]byte(in), testDNS, 9443)
		if err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", in, got, err, want)
		}
	}
}

func TestSelfDeployed(t *testing.T) {
	for exe, want := range map[string]bool{
		"/usr/local/bin/lcm": true, "/usr/local/bin/lcm.prev": true, "/usr/local/bin/lcm.failed": true,
		"/var/lib/lcm/work/lcm/bin/lcm": true, "/home/alice/src/lcm/bin/lcm": false,
		"/usr/local/bin/lcmx": false, "/usr/local/libexec/lcm-machine-helper": false,
	} {
		if got := SelfDeployed(exe); got != want {
			t.Errorf("%s: %v", exe, got)
		}
	}
}

func TestToolchainEntryOfAnotherOwnerIsRefused(t *testing.T) {
	th := newTestHost(t)
	th.fs.file("/home/alice/.local/go/src/linked", "someone else's file", 0o644, 2000, 2000)
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "src/linked is owned by user 2000, not root or the operator") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if th.fs.get(ToolchainDir+"/go.new") != nil || th.fs.get(ToolchainDir+"/go") != nil {
		t.Fatal("the refused copy was kept")
	}
}

func TestTreeBudget(t *testing.T) {
	b := &budget{entries: maxTreeEntries - 1}
	if err := b.entry(); err != nil {
		t.Fatal(err)
	}
	if err := b.entry(); err == nil {
		t.Fatal("no error beyond the entry bound")
	}
	b = &budget{}
	if err := b.add(maxTreeBytes); err != nil {
		t.Fatal(err)
	}
	if err := b.add(1); err == nil {
		t.Fatal("no error beyond the size bound")
	}
}

func TestSudoersUpdateFailureRestoresThePreviousRule(t *testing.T) {
	th := converged(t)
	previous := "# working rule\nlcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm\n"
	th.fs.file(Sudoers, previous, 0o440, 0, 0)
	fails := 1
	th.r.visudoHook = func() error {
		if fails > 0 {
			fails--
			return &ExitError{Command: "visudo -c", Code: 1, Stderr: "parse error"}
		}
		return nil
	}
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "restored the previous rule") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if n := th.fs.get(Sudoers); n == nil || string(n.data) != previous || n.mode != 0o440 || n.uid != 0 {
		t.Fatalf("rule = %+v", n)
	}
}

func TestEnvFileOfAnotherOwnerNeedsReview(t *testing.T) {
	th := converged(t)
	th.fs.file(EnvFile, string(asset("lcm.env")), 0o640, aliceUID, th.r.accounts["lcm"].GID)
	res, out, err := th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "lcm.env:         needs manual action: /etc/lcm/lcm.env is owned by user 1000") {
		t.Fatalf("%+v %v\n%s", res, err, out)
	}
	if n := th.fs.get(EnvFile); n.uid != aliceUID {
		t.Fatal("apply took the file over")
	}
}

func TestTheRunningExecutableIsInstalled(t *testing.T) {
	th := newTestHost(t)
	// The operator's build is replaced after apply started; apply installs
	// what runs, not what the path names now.
	th.fs.file(testExe, "a different build", 0o755, aliceUID, aliceUID)
	th.converge()
	if got := th.fs.content(Binary); got != exeData {
		t.Fatalf("installed %q", got)
	}

	th = newTestHost(t)
	th.host.OpenExecutable = func() (io.ReadCloser, error) { return nil, errors.New("no /proc") }
	th.fs.resetOps()
	if _, _, err := th.apply(Options{}); err == nil || !strings.Contains(err.Error(), "read the running executable: no /proc") {
		t.Fatalf("err = %v", err)
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("wrote %v", w)
	}
}

func TestOlderBuildIsWarnedAbout(t *testing.T) {
	th := converged(t)
	if _, out, _ := th.apply(Options{Check: true}); strings.Contains(out, "warning:") {
		t.Fatalf("warning for the same build:\n%s", out)
	}
	th.r.installedVersion = "lcm v2 (def) 2026-11-08T00:00:00Z\n"
	th.fs.file(Unit, "[Unit]\n", 0o644, 0, 0)
	for _, check := range []bool{true, false} {
		res, out, err := th.apply(Options{Check: check})
		if err != nil || res.Changes == 0 {
			t.Fatalf("check=%v: %+v %v\n%s", check, res, err, out)
		}
		if !strings.HasPrefix(out, "warning: the installed /usr/local/bin/lcm (lcm v2 (def) 2026-11-08T00:00:00Z) is newer than this build") ||
			!strings.Contains(out, "lcm.service (from a build older than the installed binary)") {
			t.Errorf("check=%v:\n%s", check, out)
		}
	}
	if _, ok := buildTime("lcm dev"); ok {
		t.Error("a version without a time was parsed")
	}
}

func TestMetadataFixesNeedTheCheckedFile(t *testing.T) {
	// A hard link swapped in for a directory apply fixes, after the check,
	// is not changed: the fake replaces the node between Lstat and Chmod.
	th := converged(t)
	th.fs.get(WorkDir).uid = 0
	fsys := &swapOnChown{memFS: th.fs, at: WorkDir}
	th.host.FS = fsys
	_, out, err := th.apply(Options{})
	if err == nil || !strings.Contains(err.Error(), "changed since it was checked") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if n := th.fs.get(WorkDir); n.mode.IsDir() || n.uid != 0 || n.mode.Perm() != 0o440 {
		t.Fatalf("the swapped-in file was changed: %+v", n)
	}
}

// swapOnChown replaces a directory by a root-owned regular file right
// before the first chown of it, as a hard link to such a file would.
type swapOnChown struct {
	*memFS
	at   string
	done bool
}

func (s *swapOnChown) Chown(p string, want Info, uid, gid int) error {
	if p == s.at && !s.done {
		s.done = true
		s.remove(p)
		s.file(p, "root data", 0o440, 0, 0)
	}
	return s.memFS.Chown(p, want, uid, gid)
}

func TestAdminPasswordIsCheckedBeforeItIsSent(t *testing.T) {
	for password, want := range map[string]string{
		"short":           "at least 8 characters",
		"two\nlines long": "must be one line",
	} {
		th := newTestHost(t)
		_, out, err := th.apply(Options{Password: func() (string, error) { return password, nil }})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "set it later with") {
			t.Errorf("%q: err = %v\n%s", password, err, out)
		}
		if len(th.r.called("[lcm] /usr/local/bin/lcm admin")) != 0 {
			t.Errorf("%q was sent", password)
		}
	}
}
