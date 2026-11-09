package main

import (
	"strings"
	"testing"
)

func TestHostUpgradeUsage(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{{"host", "upgrade", "extra"}, {"host", "upgrade", "--bogus"}} {
		if r := cli(args...); r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := cli("host", "upgrade", "-h"); r.code != 0 || !strings.Contains(r.stderr, "-allow-older") || !strings.Contains(r.stderr, "-check") {
		t.Errorf("upgrade -h: %+v", r)
	}
}

func TestHostUpgradeNeedsRoot(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{{"host", "upgrade"}, {"host", "upgrade", "--check"}} {
		if r := cli(args...); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
			t.Errorf("%v: %+v", args, r)
		}
	}
}
