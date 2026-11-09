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
  "components": {"machinePower": false, "securityHost": {"machineId": "0b7c-41"}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Operator != "alice" || c.Tailscale.Port != 9443 || c.Components.SecurityHost == nil || c.Components.SecurityHost.MachineID != "0b7c-41" {
		t.Fatalf("parsed %+v", c)
	}
	again, err := ParseConfig(c.Encode())
	if err != nil || again.Components.SecurityHost.MachineID != "0b7c-41" || again.Toolchains != c.Toolchains {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	c.Components = Components{}
	if !strings.Contains(string(c.Encode()), `"securityHost": null`) {
		t.Errorf("encoded:\n%s", c.Encode())
	}
	for in, want := range map[string]string{
		`[]`:   "must be a JSON object",
		`null`: "must be a JSON object",
		`{"operator":"alice","tailscale":{"port":"9443"}}`:            "cannot unmarshal",
		`{"operator":"alice","components":{"securityHost":"x"}}`:      "components.securityHost must be an object",
		`{"operator":"alice","components":{"securityHost":{"id":1}}}`: `unknown key "components.securityHost.id"`,
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
