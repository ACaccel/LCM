package hostsetup

import (
	"context"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	c, err := ParseConfig([]byte(`{
  "operator": "alice",
  "repository": "https://github.com/ACaccel/LCM.git",
  "toolchains": {"go": "/home/alice/.local/go", "node": "/home/alice/.local/node"},
  "tailscale": {"port": 9443},
  "components": {"machinePower": false, "securityHost": null,
    "security": {"machineId": "0b7c-41", "watch": {"deploy": ["/opt/extra"], "cron": []}}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	sec := c.Components.Security
	if c.Operator != "alice" || c.Tailscale.Port != 9443 || sec == nil || sec.MachineID != "0b7c-41" || sec.Watch["deploy"][0] != "/opt/extra" {
		t.Fatalf("parsed %+v", c)
	}
	if c.Components.LegacySecurityHost != nil {
		t.Errorf("a null securityHost was kept: %s", c.Components.LegacySecurityHost)
	}
	again, err := ParseConfig(c.Encode())
	if err != nil || again.Components.Security.MachineID != "0b7c-41" || again.Toolchains != c.Toolchains {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	if strings.Contains(string(c.Encode()), "securityHost") {
		t.Errorf("the replaced key is written again:\n%s", c.Encode())
	}
	c.Components = Components{}
	if !strings.Contains(string(c.Encode()), `"security": null`) {
		t.Errorf("encoded:\n%s", c.Encode())
	}
	for in, want := range map[string]string{
		`[]`:   "must be a JSON object",
		`null`: "must be a JSON object",
		`{"operator":"alice","tailscale":{"port":"9443"}}`:                                                        "cannot unmarshal",
		`{"operator":"alice","components":{"security":"x"}}`:                                                      "components.security must be an object",
		`{"operator":"alice","components":{"security":{"id":1}}}`:                                                 `unknown key "components.security.id"`,
		`{"operator":"alice","components":{"securityHost":{"machineId":"m-1"}}}`:                                  "components.securityHost was replaced by components.security",
		`{"operator":"alice","components":{"security":{"machineId":"../x"}}}`:                                     "components.security.machineId must be",
		`{"operator":"alice","components":{"security":{"machineId":"m","watch":{"probe":["/x"]}}}}`:               `"probe" is not a watched category`,
		`{"operator":"alice","components":{"security":{"machineId":"m","watch":{"deploy":["/opt/../etc"]}}}}`:     "components.security.watch.deploy: must be an absolute, clean path",
		`{"operator":"alice","components":{"security":{"machineId":"m","watch":{"deploy":["/opt/x","/opt/x"]}}}}`: "/opt/x is listed twice",
		`{"operator":"alice"}`: "repository must be",
	} {
		if _, err := ParseConfig([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestInit(t *testing.T) {
	th := newTestHost(t)
	th.fs.remove(EtcDir)
	c, err := DefaultConfig(context.Background(), th.host, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if c.Toolchains.Go != "/home/alice/.local/go" || c.Toolchains.Node != "/home/alice/.local/node" || c.Tailscale.Port != 9443 || c.Repository != DefaultRepository {
		t.Fatalf("defaults %+v", c)
	}
	if _, err := DefaultConfig(context.Background(), th.host, "bob"); err == nil {
		t.Fatal("an unknown operator was accepted")
	}
	if err := Init(th.host, ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	n := th.fs.get(ConfigPath)
	if n == nil || n.mode != 0o644 || n.uid != 0 || n.gid != 0 {
		t.Fatalf("config file %+v", n)
	}
	if got, err := LoadConfig(th.fs, ConfigPath); err != nil || got.Operator != "alice" {
		t.Fatalf("load: %+v %v", got, err)
	}
	if err := Init(th.host, ConfigPath, c); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}

	th.fs.remove(ConfigPath)
	bad := c
	bad.Tailscale.Port = 70000
	if err := Init(th.host, ConfigPath, bad); err == nil || th.fs.get(ConfigPath) != nil {
		t.Fatalf("invalid config: %v", err)
	}
	th.host.Privileged = false
	if err := Init(th.host, ConfigPath, c); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("unprivileged: %v", err)
	}
	th.host.Privileged = true
	th.host.Executable = Binary
	if err := Init(th.host, ConfigPath, c); err == nil || !strings.Contains(err.Error(), "self-deployed") {
		t.Fatalf("self-deployed binary: %v", err)
	}
	th.host.Executable = testExe
	th.fs.get(EtcDir).mode |= 0o002
	if err := Init(th.host, ConfigPath, c); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("untrusted directory: %v", err)
	}
	if err := Init(th.host, "etc/host.json", c); err == nil {
		t.Fatal("a relative path was accepted")
	}
}
