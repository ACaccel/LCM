package hostsetup

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func (th *testHost) allow(o AllowOptions) (Result, string, error) {
	th.t.Helper()
	var out bytes.Buffer
	o.Out = &out
	res, err := Allow(context.Background(), th.host, o)
	return res, out.String(), err
}

func TestAllow(t *testing.T) {
	th := converged(t)
	th.fs.dir(testInput, 0o755, aliceUID, aliceUID)
	// A hand-edited file with its own key order and an existing grant.
	orig := `{"tailscale":{"port":9443},"operator":"alice","repository":"https://github.com/ACaccel/LCM.git",
  "toolchains":{"node":"/home/alice/.local/node","go":"/home/alice/.local/go"},
  "components":{"securityHost":null,"machinePower":false},
  "runtimes":{"node24":"/home/alice/.local/node"},
  "services":{"zz":{"port":4000}}}`
	th.fs.file(ConfigPath, orig, 0o644, 0, 0)
	grant := ServiceGrant{Port: 3200, Runtimes: []string{"node24"}, Inputs: map[string]string{"assets": testInput}}

	th.fs.resetOps()
	res, out, err := th.allow(AllowOptions{Name: "web", Grant: grant, Check: true})
	if err != nil || res.Changes == 0 || !strings.Contains(out, "host.json:       would change: add services.web to /etc/lcm/host.json") ||
		!strings.Contains(out, "service web:     would change: create the system account web") {
		t.Fatalf("check: %+v %v\n%s", res, err, out)
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("check wrote %v", w)
	}

	res, out, err = th.allow(AllowOptions{Name: "web", Grant: grant})
	if err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	want := `{
  "tailscale": {
    "port": 9443
  },
  "operator": "alice",
  "repository": "https://github.com/ACaccel/LCM.git",
  "toolchains": {
    "node": "/home/alice/.local/node",
    "go": "/home/alice/.local/go"
  },
  "components": {
    "securityHost": null,
    "machinePower": false
  },
  "runtimes": {
    "node24": "/home/alice/.local/node"
  },
  "services": {
    "zz": {
      "port": 4000
    },
    "web": {
      "port": 3200,
      "runtimes": [
        "node24"
      ],
      "inputs": {
        "assets": "/srv/inputs/web/assets"
      }
    }
  }
}
`
	if got := th.fs.content(ConfigPath); got != want {
		t.Fatalf("host.json:\n%s", got)
	}
	if n := th.fs.get(ConfigPath); n.mode != 0o644 || n.uid != 0 {
		t.Errorf("host.json %+v", n)
	}
	web := th.r.accounts["web"]
	if n := th.fs.get("/var/lib/web"); n == nil || n.uid != web.UID || th.fs.get("/etc/web/env") == nil || th.fs.get("/opt/web/releases") == nil {
		t.Fatalf("service directories missing:\n%s", out)
	}
	if !strings.Contains(out, "components.deploy is false, so the deploy helper is not installed; the runtime node24 is not copied yet; complete it with: sudo bin/lcm host apply") {
		t.Errorf("no next step:\n%s", out)
	}
	if len(th.r.called("useradd")) != 2 || len(th.r.called("systemctl")) != 0 {
		t.Errorf("commands: %v", th.r.calls)
	}

	// Again: nothing to do. Then an update keeps the entry's place.
	if res, out, err := th.allow(AllowOptions{Name: "web", Grant: grant}); err != nil || res.Changes != 0 {
		t.Fatalf("again: %+v %v\n%s", res, err, out)
	}
	grant.Port = 3300
	if res, out, err := th.allow(AllowOptions{Name: "zz", Grant: ServiceGrant{Port: 4001}}); err != nil || !strings.Contains(out, "update services.zz") {
		t.Fatalf("update: %+v %v\n%s", res, err, out)
	}
	if got := th.fs.content(ConfigPath); !strings.Contains(got, "\"zz\": {\n      \"port\": 4001\n    },\n    \"web\"") {
		t.Fatalf("update moved the entry:\n%s", got)
	}
}

func TestAllowRefusals(t *testing.T) {
	grant := ServiceGrant{Port: 3200}
	for name, tc := range map[string]struct {
		setup func(th *testHost)
		name  string
		grant ServiceGrant
		want  string
	}{
		"LCM's port":      {nil, "web", ServiceGrant{Port: 7800}, "is LCM's own"},
		"unknown runtime": {nil, "web", ServiceGrant{Port: 3200, Runtimes: []string{"node24"}}, `"node24" is not a runtime`},
		"reserved name":   {nil, "lcm-web", grant, `must not start with "lcm"`},
		"login account": {func(th *testHost) {
			th.r.accounts["web"] = Account{Name: "web", UID: 1001, GID: 1001, Home: "/home/web", Shell: "/bin/bash"}
		}, "web", grant, "cannot allow web: account web exists and was not created by LCM"},
		"vendor unit":          {func(th *testHost) { th.fs.file("/lib/systemd/system/web.service", "x", 0o644, 0, 0) }, "web", grant, "cannot allow web: the unit /lib/systemd/system/web.service exists"},
		"untrusted input":      {func(th *testHost) { th.fs.dir("/tmp/in", 0o777, 0, 0) }, "web", ServiceGrant{Port: 3200, Inputs: map[string]string{"in": "/tmp/in"}}, "/tmp is writable by others"},
		"invalid config":       {func(th *testHost) { th.fs.file(ConfigPath, `{"operator":"alice"}`, 0o644, 0, 0) }, "web", grant, "fix it first"},
		"group-writable file":  {func(th *testHost) { th.fs.get(ConfigPath).mode = 0o664 }, "web", grant, "writable by group or others"},
		"self-deployed binary": {func(th *testHost) { th.host.Executable = Binary }, "web", grant, "self-deployed"},
		"not root":             {func(th *testHost) { th.host.Privileged = false }, "web", grant, "must run as root"},
	} {
		t.Run(name, func(t *testing.T) {
			th := converged(t)
			if tc.setup != nil {
				tc.setup(th)
			}
			before := th.fs.content(ConfigPath)
			th.fs.resetOps()
			_, out, err := th.allow(AllowOptions{Name: tc.name, Grant: tc.grant})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if w := th.fs.writes(); len(w) > 0 || th.fs.content(ConfigPath) != before {
				t.Fatalf("wrote %v", w)
			}
		})
	}

	t.Run("file changed since it was read", func(t *testing.T) {
		th := converged(t)
		reads := 0
		th.host.FS = &hookOpenFS{memFS: th.fs, onOpen: func(p string) {
			if p == ConfigPath {
				if reads++; reads == 2 {
					th.fs.file(ConfigPath, string(th.cfg.Encode())+" ", 0o644, 0, 0)
				}
			}
		}}
		_, out, err := th.allow(AllowOptions{Name: "web", Grant: grant})
		if err == nil || !strings.Contains(err.Error(), "changed since it was read") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if strings.Contains(th.fs.content(ConfigPath), "services") || th.r.accounts["web"].Name != "" {
			t.Fatal("changed the host after the file changed")
		}
	})
}

// hookOpenFS calls onOpen before each Open.
type hookOpenFS struct {
	*memFS
	onOpen func(p string)
}

func (h *hookOpenFS) Open(p string) (io.ReadCloser, error) {
	h.onOpen(p)
	return h.memFS.Open(p)
}

func (h *hookOpenFS) OpenTree(root string) (Tree, error) { return fsTree{fs: h, root: root}, nil }
