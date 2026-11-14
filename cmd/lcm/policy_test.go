package main

import (
	"strings"
	"testing"
)

func TestPolicyPauseResumeCommand(t *testing.T) {
	s := newServer(t)
	s.cli("", "apply", "-f", writeFile(t, t.TempDir(), "p.yaml", polDoc("echo", manualEcho))).want(t, 0)

	s.cli("", "policy", "pause", "echo").want(t, 0, "pause echo: request accepted")
	paused := s.cli("", "get", "policies", "-o", "json").want(t, 0)
	if !strings.Contains(paused.stdout, `"paused": true`) {
		t.Fatalf("policy not paused:\n%s", paused.stdout)
	}
	s.cli("", "policy", "resume", "echo").want(t, 0, "resume echo: request accepted")
	resumed := s.cli("", "get", "policies", "-o", "json").want(t, 0)
	if !strings.Contains(resumed.stdout, `"paused": false`) {
		t.Fatalf("policy not resumed:\n%s", resumed.stdout)
	}

	s.cli("", "policy", "pause", "ghost").want(t, 1)
	s.cli("", "policy").want(t, 2)
	s.cli("", "policy", "pause").want(t, 2)
	s.cli("", "policy", "stop", "echo").want(t, 2)
	s.cli("", "policy", "pause", "a", "b").want(t, 2)
}
