package main

import (
	"strings"
	"testing"
)

func TestHostInitComponentFlagUsage(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{
		{"host", "init", "--operator", "alice", "--backup-dir", "/var/backups/lcm-services"},
		{"host", "init", "--operator", "alice", "--deploy", "--backup-tool", "pg_dump=/usr/bin/pg_dump"},
		{"host", "init", "--operator", "alice", "--deploy", "--backup-dir", "/b", "--backup-tool", "pg_dump"},
		{"host", "init", "--operator", "alice", "--deploy", "--backup-dir", "/b", "--backup-tool", "pg_dump=/a", "--backup-tool", "pg_dump=/b"},
		{"host", "init", "--operator", "alice", "--runtime", "node24"},
		{"host", "init", "--operator", "alice", "--runtime", "=/x"},
		{"host", "init", "--operator", "alice", "--ingress-domain", "example.org"},
		{"host", "init", "--operator", "alice", "--ingress-email", "ops@example.org"},
		{"host", "init", "--operator", "alice", "--ingress-webroot", "/var/www/acme"},
	} {
		if r := cli(args...); r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	args := []string{"host", "init", "--operator", "alice", "--deploy", "--backup-dir", "/var/backups/lcm-services", "--backup-tool", "pg_dump=/usr/bin/pg_dump",
		"--runtime", "node24=/home/alice/.local/node", "--ingress-domain", "example.org", "--ingress-email", "ops@example.org"}
	if r := cli(args...); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged: %+v", r)
	}
}
