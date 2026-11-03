package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/security"
)

func TestSecurityHTTPKonataRestartIntegration(t *testing.T) {
	t.Setenv("LCM_SECRET_KONATA_NOTIFY_KEY", "synthetic-relay-key")
	var mu sync.Mutex
	var paths []string
	fail := true
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "PUT" || r.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
			t.Errorf("invalid relay request")
		}
		paths = append(paths, r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["summary"] == "" || body["occurredAt"] == "" {
			t.Error("missing sanitized incident context")
		}
		if fail {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":false,"messageId":"one"}`)
	}))
	defer relay.Close()
	configure := func(c *config.Config) {
		c.SecurityEnabled = true
		c.SecurityMachineID = "configured-identity"
		c.SecurityKonataURL = relay.URL + "/notifications/security-alerts"
		c.SecurityTokenSecret = "konata-notify-key"
	}
	h := newHarness(t, func(o *testOpts) { o.configure = configure })
	_, err := h.app.Machines.Put(h.ctx, []byte(`{"apiVersion":"lcm/v1","kind":"Machine","metadata":{"name":"workstation"},"spec":{}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	record, err := h.app.Machines.Get(h.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := h.app.Auth.CreateToken(h.ctx, "collector", auth.ScopeIngest)
	if err != nil {
		t.Fatal(err)
	}
	_, admin, err := h.app.Auth.CreateToken(h.ctx, "operator", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, tok string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want%d: %s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	event := security.Event{EventID: "integration-event", MachineID: record.ID, Source: "auditd", Fingerprint: "sudoers-write", Status: "firing", Severity: "critical", Summary: "Synthetic protected configuration modification", ObservedAt: h.clk.Now()}
	raw, _ := json.Marshal(event)
	w := request("POST", "/hooks/security", string(raw), token, 202)
	var receipt security.Receipt
	if err = json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	request("GET", "/security/status", "", token, 403)
	request("GET", "/security/incidents", "", token, 403)
	request("GET", "/security/status", "", admin, 200)
	traffic := security.Traffic{MachineID: record.ID, WindowStart: h.clk.Now().Add(-time.Minute), WindowEnd: h.clk.Now(), Category: "auth", Requests: 2, RequestTimeSumMs: 20, RequestTimeMaxMs: 15}
	trafficRaw, _ := json.Marshal(traffic)
	request("POST", "/hooks/security/traffic", string(trafficRaw), token, 202)
	request("GET", "/security/traffic", "", token, 403)
	tw := request("GET", "/security/traffic", "", admin, 200)
	if !strings.Contains(tw.Body.String(), `"requests":2`) {
		t.Fatal("missing typed traffic readback")
	}

	request("POST", "/hooks/security", `{"unknown":"field"}`, token, 400)
	request("POST", "/hooks/security", strings.Repeat("a", security.MaxBodyBytes+1), token, 400)
	request("POST", "/hooks/security", `{"summary":"`+strings.Repeat("a", security.MaxBodyBytes)+`"}`, token, 413)
	if err = h.app.Security.Deliver(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Close the database and HTTP components, then reopen the actual file.
	if err = h.app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Minute)
	h.app = h.open(testOpts{dir: h.dir, clk: h.clk, runner: h.runner, configure: configure})
	mu.Lock()
	fail = false
	mu.Unlock()
	if err = h.app.Security.Deliver(h.ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(paths) != 2 || paths[0] != paths[1] || !strings.HasSuffix(paths[0], receipt.IncidentID) {
		t.Errorf("unstable relay identity: %v", paths)
	}
	mu.Unlock()
	event.EventID = "integration-recovery"
	event.Status = "resolved"
	event.Severity = "info"
	event.ObservedAt = h.clk.Now()
	raw, _ = json.Marshal(event)
	request("POST", "/hooks/security", string(raw), token, 202)
	if err = h.app.Security.Deliver(h.ctx); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/security/incidents?limit=1", "", admin, 200)
	if !strings.Contains(w.Body.String(), `"state":"resolved"`) || !strings.Contains(w.Body.String(), `"delivery":"sent"`) {
		t.Fatalf("missing recovery: %s", w.Body.String())
	}
	request("GET", "/security/incidents?cursor=invalid", "", admin, 400)
}

// The actual Machine manager resolves unprefixed strings as names, not IDs.
func TestSecurityPollUsesRegisteredMachineIdentity(t *testing.T) {
	h := newHarness(t)
	_, err := h.app.Machines.Put(h.ctx, []byte(`{"apiVersion":"lcm/v1","kind":"Machine","metadata":{"name":"workstation"},"spec":{}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	record, err := h.app.Machines.Get(h.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if record.ID == record.Name {
		t.Fatal("fixture must distinguish Machine name from ID")
	}
	if err = h.app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	configure := func(c *config.Config) { c.SecurityEnabled = true; c.SecurityMachineID = record.ID }
	h.app = h.open(testOpts{dir: h.dir, clk: h.clk, runner: h.runner, configure: configure})
	for range 2 {
		if err = h.app.Security.Poll(h.ctx, h.app.Machines, h.app.Monitor); err != nil {
			t.Fatal(err)
		}
		status, statusErr := h.app.Security.Status(h.ctx)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.LastPollAt == nil || status.LastError != "" {
			t.Fatalf("native poll did not advance: %+v", status)
		}
		h.clk.Advance(time.Minute)
	}
}
