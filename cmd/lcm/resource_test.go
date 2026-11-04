package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func syntheticServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &server{t: t, url: ts.URL, token: "synthetic-token"}
}

func writeArgs(command ...string) []string {
	return append(command, "--version", "7", "--reason", "scheduled work", "--confirm", "workstation", "--acknowledge")
}

func TestResourceCLIRequests(t *testing.T) {
	tests := []struct {
		name                string
		args                []string
		method, path, query string
		body                map[string]any
		write               bool
		response            string
	}{
		{name: "observe", args: []string{"observe", "workstation"}, method: "POST", path: "/resources/workstation/observe", response: `{"name":"workstation","status":{"health":"unknown"}}`},
		{name: "history", args: []string{"history", "workstation", "--limit", "2", "--cursor", "opaque+cursor="}, method: "GET", path: "/resources/workstation/history", query: "cursor=opaque%2Bcursor%3D&limit=2", response: `{"items":[],"nextCursor":"next-page"}`},
		{name: "events", args: []string{"events", "workstation"}, method: "GET", path: "/resources/workstation/events", query: "limit=100", response: `{"items":[]}`},
		{name: "capabilities", args: []string{"capabilities", "workstation"}, method: "GET", path: "/resources/workstation/capabilities", response: `{"items":[{"action":"power-on","available":false}]}`},
		{name: "maintenance status", args: []string{"maintenance", "workstation"}, method: "GET", path: "/resources/workstation/maintenance", response: `{"maintenance":null}`},
		{name: "maintenance enter", args: append(writeArgs("maintenance", "workstation", "enter"), "--preview-hash", strings.Repeat("a", 64)), method: "POST", path: "/resources/workstation/maintenance", body: map[string]any{"reason": "scheduled work", "acknowledged": true, "previewHash": strings.Repeat("a", 64)}, write: true, response: `{"maintenance":{"state":"active"}}`},
		{name: "maintenance exit", args: writeArgs("maintenance", "workstation", "exit"), method: "POST", path: "/resources/workstation/maintenance/exit", body: map[string]any{"reason": "scheduled work"}, write: true},
		{name: "operations", args: []string{"operations", "workstation"}, method: "GET", path: "/resources/workstation/operations", query: "limit=100", response: `{"items":[]}`},
		{name: "operation", args: []string{"operation", "workstation", "op-1"}, method: "GET", path: "/resources/workstation/operations/op-1", response: `{"id":"op-1","state":"indeterminate"}`},
		{name: "reboot", args: append(writeArgs("reboot", "workstation"), "--idempotency-key", "operator-key"), method: "POST", path: "/resources/workstation/operations", body: map[string]any{"action": "reboot", "idempotencyKey": "operator-key"}, write: true, response: `{"id":"op-1","state":"observing","unresolved":true}`},
		{name: "shutdown", args: append(writeArgs("shutdown", "workstation"), "--idempotency-key", "operator-key"), method: "POST", path: "/resources/workstation/operations", body: map[string]any{"action": "graceful-shutdown", "idempotencyKey": "operator-key"}, write: true, response: `{"id":"op-1","state":"indeterminate","unresolved":true}`},
		{name: "resolve", args: writeArgs("resolve", "workstation", "op-1"), method: "POST", path: "/resources/workstation/operations/op-1/resolve", body: map[string]any{"reason": "scheduled work"}, write: true, response: `{"id":"op-1","state":"indeterminate","unresolved":false}`},
		{name: "retire", args: writeArgs("retire", "workstation"), method: "POST", path: "/resources/workstation/phase", body: map[string]any{"phase": "retired", "reason": "scheduled work", "version": float64(7)}, write: true, response: `{"name":"workstation","phase":"retired"}`},
		{name: "resume", args: writeArgs("resume", "workstation"), method: "POST", path: "/resources/workstation/phase", body: map[string]any{"phase": "managed", "reason": "scheduled work", "version": float64(7)}, write: true, response: `{"name":"workstation","phase":"managed"}`},
		{name: "unmanage", args: writeArgs("unmanage", "workstation"), method: "POST", path: "/resources/workstation/phase", body: map[string]any{"phase": "unmanaged", "reason": "scheduled work", "version": float64(7)}, write: true, response: `{"name":"workstation","phase":"unmanaged"}`},
		{name: "impact", args: []string{"impact", "workstation"}, method: "GET", path: "/resources/workstation/impact", response: `{"resource":"workstation"}`},
		{name: "reboot acknowledging impact", args: append(writeArgs("reboot", "workstation"), "--idempotency-key", "operator-key", "--acknowledge-impact"), method: "POST", path: "/resources/workstation/operations", body: map[string]any{"action": "reboot", "idempotencyKey": "operator-key", "acknowledgeImpact": true}, write: true, response: `{"id":"op-1","state":"observing","unresolved":true}`},
		{name: "retire acknowledging impact", args: append(writeArgs("retire", "workstation"), "--acknowledge-impact"), method: "POST", path: "/resources/workstation/phase", body: map[string]any{"phase": "retired", "reason": "scheduled work", "version": float64(7), "acknowledgeImpact": true}, write: true, response: `{"name":"workstation","phase":"retired"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != tt.method || r.URL.Path != "/api/v1"+tt.path || r.URL.RawQuery != tt.query {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("missing token")
				}
				match := ""
				if tt.write {
					match = `"7"`
				}
				if r.Header.Get("If-Match") != match {
					t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var body map[string]any
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Error(err)
					}
				}
				if !reflect.DeepEqual(body, tt.body) {
					t.Errorf("body: %v, want %v", body, tt.body)
				}
				if tt.body != nil && r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing JSON content type")
				}
				if tt.response == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			})
			got := s.cli("", append([]string{"resource"}, tt.args...)...).want(t, 0)
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
			if strings.Contains(got.stdout, "succeeded") || strings.Contains(got.stdout, "cancelled") {
				t.Fatalf("false completion: %s", got.stdout)
			}
			if strings.Contains(tt.response, `"id":"op-1"`) && (!strings.Contains(got.stdout, "op-1") || !strings.Contains(got.stdout, `"state"`)) {
				t.Fatalf("missing operation state: %s", got.stdout)
			}
		})
	}
}

func TestResourceCLIRejectsUnconfirmedWrites(t *testing.T) {
	var calls atomic.Int32
	s := syntheticServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	base := []string{"resource", "reboot", "workstation", "--version", "7", "--reason", "scheduled work", "--confirm", "workstation", "--acknowledge", "--idempotency-key", "stable-key"}
	for _, remove := range []string{"--version", "--reason", "--confirm", "--acknowledge", "--idempotency-key"} {
		t.Run(remove, func(t *testing.T) {
			var args []string
			for i := 0; i < len(base); i++ {
				if base[i] == remove {
					if remove != "--acknowledge" {
						i++
					}
					continue
				}
				args = append(args, base[i])
			}
			s.cli("", args...).want(t, 2)
		})
	}
	for _, args := range [][]string{
		{"resource", "maintenance", "workstation", "enter"},
		{"resource", "maintenance", "workstation", "exit"},
		{"resource", "retire", "workstation"},
		{"resource", "resume", "workstation"},
		{"resource", "unmanage", "workstation"},
		{"resource", "resolve", "workstation", "op-1"},
		{"resource", "operation", "workstation"},
		{"resource", "observe", "workstation", "extra"},
		{"resource", "events", "workstation", "--limit", "0"},
		{"resource", "events", "workstation", "--limit", "200"},
		{"resource", "events", "workstation", "-o", "table"},
		{"resource", "maintenance", "workstation", "unsupported"},
		{"resource", "cancel", "workstation", "op-1"},
		{"machine-helper", "arbitrary-argument"},
		{"resource", "action", "workstation"},
		{"resource", "action", "workstation", "start", "extra"},
		{"machine", "observe", "workstation"},
		{"action", "workstation", "start"},
	} {
		s.cli("", args...).want(t, 2)
	}
	bad := append([]string{}, base...)
	for i, a := range bad {
		if a == "--confirm" {
			bad[i+1] = "another-machine"
		}
	}
	s.cli("", bad...).want(t, 2)
	if calls.Load() != 0 {
		t.Fatalf("unconfirmed commands made %d requests", calls.Load())
	}
}

func TestResourceCLIErrorsDoNotRetryOrClaimSuccess(t *testing.T) {
	for _, status := range []int{401, 409, 422, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"code":"precondition_failed","message":"synthetic rejection"}}`)
			})
			args := append(writeArgs("resource", "shutdown", "workstation"), "--idempotency-key", "stable-key")
			got := s.cli("", args...).want(t, 1)
			if calls.Load() != 1 || got.stdout != "" || !strings.Contains(got.stderr, "synthetic rejection") || !strings.Contains(got.stderr, "same --idempotency-key") {
				t.Fatalf("error response: calls=%d %+v", calls.Load(), got)
			}
		})
	}
	var calls atomic.Int32
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			return
		}
		conn, _, err := h.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	args := append(writeArgs("resource", "reboot", "workstation"), "--idempotency-key", "stable-key")
	got := s.cli("", args...).want(t, 1)
	if calls.Load() != 1 || got.stdout != "" || !strings.Contains(got.stderr, "ambiguous") {
		t.Fatalf("ambiguous submission: calls=%d %+v", calls.Load(), got)
	}
}

func TestGetResourcesAndResourceYAML(t *testing.T) {
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/resources":
			if r.URL.Query().Get("type") != "machine" {
				t.Errorf("type filter not sent: %s", r.URL)
			}
			_, _ = io.WriteString(w, `{"items":[{"name":"workstation","type":"machine","adapter":"local","phase":"managed","state":"unknown","health":"unknown","version":7}],"nextCursor":null}`)
		case "/api/v1/resources/workstation":
			_, _ = io.WriteString(w, `{"name":"workstation","type":"machine","adapter":"local","phase":"managed","state":"online","health":{"status":"healthy"},"version":7,"document":{"kind":"Resource"}}`)
		case "/api/v1/resources/workstation/operations/op-1":
			_, _ = io.WriteString(w, `{"id":"op-1","state":"indeterminate","unresolved":true}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	})
	s.cli("", "get", "resources", "--type", "machine").want(t, 0, "NAME", "TYPE", "ADAPTER", "PHASE", "STATE", "HEALTH", "VERSION", "workstation", "machine", "local", "unknown")
	s.cli("", "get", "resources", "workstation").want(t, 0, "online", "healthy")
	s.cli("", "get", "resources", "workstation", "-o", "json").want(t, 0, `"document"`, `"kind": "Resource"`)
	s.cli("", "resource", "operation", "workstation", "op-1", "-o", "yaml").want(t, 0, "id: op-1", "state: indeterminate", "unresolved: true")
	s.cli("", "get", "policies", "--type", "machine").want(t, 2)
}

func TestResourcePowerResponsesRequireOperationEvidence(t *testing.T) {
	for _, response := range []string{"", `{}`, `{"id":"op-1"}`, `{"id":1,"state":"observing"}`, `{"id":"op-1","state":"failed"}`, `{"id":"op-1","state":"cancelled"}`, `{broken`} {
		t.Run(response, func(t *testing.T) {
			s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, response)
			})
			args := append(writeArgs("resource", "reboot", "workstation"), "--idempotency-key", "stable-key")
			got := s.cli("", args...).want(t, 1)
			if strings.Contains(got.stdout, "request accepted") {
				t.Fatalf("claimed acceptance without operation evidence: %+v", got)
			}
			if strings.Contains(response, `"state":"failed"`) && (!strings.Contains(got.stdout, "op-1") || !strings.Contains(got.stdout, "failed")) {
				t.Fatalf("failed operation was hidden: %+v", got)
			}
		})
	}
}

func TestResourceCLIApplyValidateExport(t *testing.T) {
	const document = "apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation}\nspec: {type: machine, adapter: local}\n"
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/api/v1/apply", "/api/v1/validate":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || string(raw) != document || r.Header.Get("Content-Type") != "application/yaml" {
				t.Errorf("document request: %s %s", r.Method, raw)
			}
			if r.URL.Path == "/api/v1/apply" {
				if r.URL.Query().Get("dryRun") != "true" {
					t.Error("dry-run not preserved")
				}
				_, _ = io.WriteString(w, `{"items":[{"index":0,"kind":"Resource","name":"workstation","action":"create"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"kind":"Resource","name":"workstation","valid":true}`)
			}
		case "/api/v1/export":
			if r.Method != "GET" || r.URL.Query().Get("kind") != "resource" || r.URL.Query().Get("name") != "workstation" {
				t.Errorf("export: %s %s", r.Method, r.URL)
			}
			_, _ = io.WriteString(w, document)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(404)
		}
	})
	s.cli(document, "apply", "-f", "-", "--dry-run").want(t, 0, "Resource/workstation", "dry run")
	s.cli(document, "validate", "-f", "-").want(t, 0, "Resource/workstation", "1 documents, 1 valid")
	s.cli("", "export", "resource/workstation").want(t, 0, "kind: Resource")
}
