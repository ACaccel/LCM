package main

import (
	"strings"
	"testing"
)

func TestHostAllowUsage(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{{"host", "allow"}, {"host", "allow", "web"}, {"host", "allow", "web", "other", "--port", "3200"},
		{"host", "allow", "web", "--port", "3200", "--input", "assets"}, {"host", "allow", "web", "--port", "3200", "--input", "a=/x", "--input", "a=/y"},
		{"host", "allow", "web", "--port", "3200", "--static", "/srv/web"}, {"host", "allow", "web", "--port", "3200", "--external"},
		{"host", "allow", "site", "--external", "--route", "/"}, {"host", "allow", "site", "--route", "/", "--static", "/srv/site"},
		{"host", "allow", "p", "--port", "3200", "--instance", "a"}, {"host", "allow", "p", "--instance", "a:x"},
		{"host", "allow", "p", "--instance", "a", "--instance", "a:4000"}, {"host", "allow", "p", "--port", "3200", "--instance-group", "a=g"},
		{"host", "allow", "p", "--instance", "a", "--instance-group", "b=g"}, {"host", "allow", "p", "--instance", "a", "--instance-group", "a"}} {
		if r := cli(args...); r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := cli("host", "allow", "web", "--port", "3200", "--runtime", "node24", "--input", "assets=/srv/a"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged: %+v", r)
	}
	if r := cli("host", "allow", "p", "--instance", "a:4100", "--instance", "b", "--instance-group", "b=g", "--group", "s", "--after", "db.service", "--wants", "db.service"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged instances: %+v", r)
	}
	if r := cli("host", "allow", "site", "--external", "--route", "/", "--static", "/srv/site"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged external: %+v", r)
	}
	for _, args := range [][]string{{"host", "cert"}, {"host", "cert", "renew"}, {"host", "ingress"}, {"host", "ingress", "remove"}, {"host", "cert", "issue", "x"}} {
		if r := cli(args...); r.code != 2 {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := cli("host", "ingress", "revert"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged revert: %+v", r)
	}
	if r := cli("host", "ingress", "revert", "--check"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
		t.Errorf("unprivileged revert --check: %+v", r)
	}
}
