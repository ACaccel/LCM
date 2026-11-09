package main

import (
	"os"
	"strings"
	"testing"
)

func TestDeployHelperCommand(t *testing.T) {
	_, _, cli := testHostRoot(t)
	if r := cli("deploy-helper", "build"); r.code != 2 || !strings.Contains(r.stderr, "accepts no arguments") {
		t.Errorf("with an argument: %+v", r)
	}
	if os.Geteuid() != 0 {
		if r := cli("deploy-helper"); r.code != 1 || !strings.Contains(r.stderr, "must run as root") || r.stdout != "" {
			t.Errorf("unprivileged: %+v", r)
		}
	}
}
