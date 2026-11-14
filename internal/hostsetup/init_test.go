package hostsetup

import (
	"context"
	"strings"
	"testing"
)

func TestInitComponents(t *testing.T) {
	th := newTestHost(t)
	th.fs.remove(EtcDir)
	base, err := DefaultConfig(context.Background(), th.host, "alice")
	if err != nil {
		t.Fatal(err)
	}
	good := base
	good.Components.Deploy = true
	good.Runtimes = map[string]string{"node24": "/home/alice/.local/node"}
	good.Backup = &Backup{Dir: "/var/backups/lcm-services", Tools: map[string]string{"pg_dump": "/usr/bin/pg_dump"}}
	good.Components.Ingress = &Ingress{Domain: "example.org", TLS: IngressTLS{Email: "ops@example.org"}}

	for name, mutate := range map[string]func(c *Config){
		"unknown backup tool": func(c *Config) { c.Backup.Tools = map[string]string{"mysqldump": "/usr/bin/mysqldump"} },
		"tool name mismatch":  func(c *Config) { c.Backup.Tools = map[string]string{"pg_dump": "/usr/bin/other"} },
		"system backup dir":   func(c *Config) { c.Backup.Dir = "/var" },
		"relative runtime":    func(c *Config) { c.Runtimes = map[string]string{"node24": "node"} },
		"bad runtime name":    func(c *Config) { c.Runtimes = map[string]string{"Node 24": "/opt/x"} },
		"bad ingress domain":  func(c *Config) { c.Components.Ingress.Domain = "not a domain" },
		"bad ingress email":   func(c *Config) { c.Components.Ingress.TLS.Email = "nobody" },
		"public webroot":      func(c *Config) { c.Components.Ingress.ACMEWebroot = "/etc/acme" },
		"empty backup dir":    func(c *Config) { c.Backup.Dir = "" },
	} {
		c := good
		bk := *good.Backup
		ing := *good.Components.Ingress
		c.Backup, c.Components.Ingress = &bk, &ing
		mutate(&c)
		if err := Init(th.host, ConfigPath, c); err == nil || th.fs.get(ConfigPath) != nil {
			t.Errorf("%s: written or accepted (%v)", name, err)
		}
	}

	if err := Init(th.host, ConfigPath, good); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(th.fs, ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Components.Deploy || got.Runtimes["node24"] != "/home/alice/.local/node" || got.Backup.Dir != "/var/backups/lcm-services" ||
		got.Backup.Tools["pg_dump"] != "/usr/bin/pg_dump" || got.Components.Ingress.Domain != "example.org" || got.Components.Ingress.TLS.Email != "ops@example.org" {
		t.Fatalf("loaded %+v", got)
	}
	if err := Init(th.host, ConfigPath, good); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}
}
