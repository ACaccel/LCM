package main

import (
	"os"
	"strings"
	"testing"
)

func TestBackupWorkerCommand(t *testing.T) {
	_, _, cli := testHostRoot(t)
	if r := cli("backup-worker"); r.code != 2 || !strings.Contains(r.stderr, "takes the input and the output directory") {
		t.Errorf("without arguments: %+v", r)
	}
	r := cli("backup-worker", t.TempDir(), t.TempDir())
	if r.code != 1 || r.stdout != "" {
		t.Errorf("without a specification: %+v", r)
	}
	if os.Geteuid() == 0 && !strings.Contains(r.stderr, "must not run as root") {
		t.Errorf("as root: %+v", r)
	}
}

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
