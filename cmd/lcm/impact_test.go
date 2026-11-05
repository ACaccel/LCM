package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// impactSite is a synthetic write whose impact needs acknowledging; it
// records the acknowledgeImpact it receives.
func impactSite(t *testing.T) (*site, *[]any) {
	t.Helper()
	var got []any
	st := &site{impactRequired: true, write: func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = append(got, body["acknowledgeImpact"])
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"op-1","state":"observing","unresolved":true}`)
	}}
	return st, &got
}

func TestResourceCLIAnswerAcceptsImpactOnATerminal(t *testing.T) {
	args := []string{"resource", "reboot", "workstation", "--reason", "kernel update", "--confirm", "workstation"}

	st, got := impactSite(t)
	r := st.serve(t).cliTTY("y\n", args...).want(t, 0, "op-1")
	if len(*got) != 1 || (*got)[0] != true {
		t.Fatalf("acknowledgeImpact = %v, want one request with true", *got)
	}
	for _, want := range []string{"impact: nas (controlPath workstation)", "Proceed and accept the impact above? [y/N]"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}

	// Anything but yes sends nothing.
	st, got = impactSite(t)
	st.serve(t).cliTTY("n\n", args...).want(t, 1)
	if len(*got) != 0 {
		t.Fatalf("a declined prompt sent %v", *got)
	}

	// An impact-free write keeps the plain prompt and sends no acknowledgement.
	plain := &site{write: func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["acknowledgeImpact"]; ok {
			t.Errorf("body carries acknowledgeImpact: %v", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"op-1","state":"observing","unresolved":true}`)
	}}
	r = plain.serve(t).cliTTY("y\n", args...).want(t, 0)
	if strings.Contains(r.stderr, "accept the impact") {
		t.Errorf("impact-free write asked about impact:\n%s", r.stderr)
	}
}

func TestResourceCLIYesDoesNotAcceptImpact(t *testing.T) {
	base := []string{"resource", "reboot", "workstation", "--reason", "kernel update", "--confirm", "workstation"}

	// --yes skips the prompt, so it cannot stand in for reading the impact,
	// on a terminal or off one.
	for name, run := range map[string]func(s *server, args ...string) result{
		"terminal": func(s *server, args ...string) result { return s.cliTTY("y\n", args...) },
		"script":   func(s *server, args ...string) result { return s.cli("", args...) },
	} {
		t.Run(name, func(t *testing.T) {
			st, got := impactSite(t)
			r := run(st.serve(t), append(base, "--yes")...).want(t, 2)
			if len(*got) != 0 || !strings.Contains(r.stderr, "--acknowledge-impact") {
				t.Fatalf("--yes without the flag: sent %v\n%s", *got, r.stderr)
			}
			st, got = impactSite(t)
			run(st.serve(t), append(base, "--yes", "--acknowledge-impact")...).want(t, 0)
			if len(*got) != 1 || (*got)[0] != true {
				t.Fatalf("--yes with the flag sent %v", *got)
			}
		})
	}

	// Without a terminal and without --yes nobody can answer.
	st, got := impactSite(t)
	r := st.serve(t).cli("y\n", base...).want(t, 2)
	if len(*got) != 0 || !strings.Contains(r.stderr, "--acknowledge-impact") {
		t.Fatalf("script without --yes: sent %v\n%s", *got, r.stderr)
	}
}

// dependentServices applies db and a web service that depends on it, both
// started, so that stopping db has an impact.
func dependentServices(t *testing.T) *server {
	t.Helper()
	s := newServer(t)
	dir := t.TempDir()
	labeled := func(name, extra string) string {
		return strings.Replace(svcDoc(name, extra), "metadata: { name: "+name+" }", "metadata: { name: "+name+", labels: { tier: web } }", 1)
	}
	s.cli("", "apply", "-f", writeFile(t, dir, "db.yaml", labeled("db", ""))).want(t, 0)
	s.cli("", "apply", "-f", writeFile(t, dir, "web.yaml", labeled("web", "  relations: [{ type: dependsOn, target: db }]\n"))).want(t, 0)
	s.cli("", "resource", "group-action", "--label", "tier=web", "start", "--wait").want(t, 0)
	return s
}

func TestResourceActionCLIAnswerAcceptsImpact(t *testing.T) {
	s := dependentServices(t)

	r := s.cliTTY("n\n", "resource", "action", "db", "stop").want(t, 1)
	if !strings.Contains(r.stderr, "impact: web (dependsOn db)") || !strings.Contains(r.stderr, "[y/N]") {
		t.Fatalf("declined: %+v", r)
	}
	s.cli("", "get", "resources", "db").want(t, 0, "running")

	// --yes cannot accept an impact that was not read.
	r = s.cliTTY("y\n", "resource", "action", "db", "stop", "--yes").want(t, 1)
	if !strings.Contains(r.stderr, "--acknowledge-impact") {
		t.Fatalf("--yes: %+v", r)
	}
	r = s.cli("", "resource", "action", "db", "stop").want(t, 1)
	if !strings.Contains(r.stderr, "--acknowledge-impact") {
		t.Fatalf("script: %+v", r)
	}
	s.cli("", "get", "resources", "db").want(t, 0, "running")

	s.cliTTY("y\n", "resource", "action", "db", "stop", "--wait").want(t, 0, "succeeded")
	s.cli("", "get", "resources", "db").want(t, 0, "stopped")

	// An action without a guarded impact never asks.
	r = s.cliTTY("", "resource", "action", "web", "stop", "--wait").want(t, 0, "succeeded")
	if strings.Contains(r.stderr, "[y/N]") {
		t.Fatalf("asked about an impact-free action: %s", r.stderr)
	}
}

func TestGroupActionCLIAnswerAcceptsImpact(t *testing.T) {
	s := dependentServices(t)
	group := []string{"resource", "group-action", "--label", "tier=web", "stop"}

	r := s.cliTTY("n\n", append(group, "--wait")...).want(t, 1)
	if !strings.Contains(r.stderr, "1. stop web") || !strings.Contains(r.stderr, "2. stop db") || !strings.Contains(r.stderr, "[y/N]") {
		t.Fatalf("declined: %+v", r)
	}
	s.cli("", "get", "resources", "db").want(t, 0, "running")

	r = s.cliTTY("y\n", append(group, "--yes")...).want(t, 1)
	if !strings.Contains(r.stderr, "--acknowledge-impact") {
		t.Fatalf("--yes: %+v", r)
	}
	r = s.cli("", append(group, "--wait")...).want(t, 1)
	if !strings.Contains(r.stderr, "--acknowledge-impact") {
		t.Fatalf("script: %+v", r)
	}
	s.cli("", "get", "resources", "db").want(t, 0, "running")

	s.cliTTY("y\n", append(group, "--wait")...).want(t, 0, "web: succeeded", "db: succeeded")
	s.cli("", "get", "resources", "db").want(t, 0, "stopped")
}
