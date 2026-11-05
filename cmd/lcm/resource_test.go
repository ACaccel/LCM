package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticServer(t *testing.T, h http.HandlerFunc) *server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &server{t: t, url: ts.URL, token: "synthetic-token"}
}

// site is the synthetic server of a write: it answers the reads a write
// makes (the resource, its impact, maintenance and preview) and passes
// every other request to write, counting them.
type site struct {
	typ            string
	maintenance    string
	impactRequired bool
	writes         atomic.Int32
	write          http.HandlerFunc
}

var previewHash = strings.Repeat("a", 64)

func (s *site) serve(t *testing.T) *server {
	t.Helper()
	if s.typ == "" {
		s.typ = "machine"
	}
	if s.maintenance == "" {
		s.maintenance = "null"
	}
	return syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing token")
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/resources/workstation":
				_, _ = io.WriteString(w, `{"name":"workstation","type":"`+s.typ+`","version":7}`)
				return
			case "/api/v1/resources/workstation/impact":
				if s.impactRequired {
					_, _ = io.WriteString(w, `{"resource":"workstation","dependents":[],"controlDependents":[{"name":"nas","via":"workstation"}],"unresolved":[],"acknowledgementRequired":true}`)
					return
				}
				_, _ = io.WriteString(w, `{"resource":"workstation","dependents":[],"controlDependents":[],"unresolved":[],"acknowledgementRequired":false}`)
				return
			case "/api/v1/resources/workstation/maintenance":
				_, _ = io.WriteString(w, `{"maintenance":`+s.maintenance+`}`)
				return
			case "/api/v1/resources/workstation/maintenance/preview":
				_, _ = io.WriteString(w, `{"hash":"`+previewHash+`","workloads":[{"name":"cache","version":3,"state":"running"}]}`)
				return
			}
		}
		s.writes.Add(1)
		s.write(w, r)
	})
}

// cliTTY runs the CLI with stdin as a terminal answering answer.
func (s *server) cliTTY(answer string, args ...string) result {
	s.t.Helper()
	var out, errb bytes.Buffer
	vars := map[string]string{"LCM_SERVER": s.url, "LCM_TOKEN": s.token}
	a := &app{
		stdin: strings.NewReader(answer), stdout: &out, stderr: &errb,
		getenv: func(k string) string { return vars[k] }, poll: time.Millisecond,
		terminal: func() bool { return true },
	}
	code := a.run(context.Background(), args)
	return result{code, out.String(), errb.String()}
}

func TestResourceCLIReads(t *testing.T) {
	tests := []struct {
		name                string
		args                []string
		method, path, query string
		response            string
	}{
		{name: "observe", args: []string{"observe", "workstation"}, method: "POST", path: "/resources/workstation/observe", response: `{"name":"workstation","status":{"health":"unknown"}}`},
		{name: "history", args: []string{"history", "workstation", "--limit", "2", "--cursor", "opaque+cursor="}, method: "GET", path: "/resources/workstation/history", query: "cursor=opaque%2Bcursor%3D&limit=2", response: `{"items":[],"nextCursor":"next-page"}`},
		{name: "history range", args: []string{"history", "workstation", "--from", "2026-11-05T08:00:00+08:00", "--to", "2026-11-05T09:30:00Z", "--step", "5m"}, method: "GET", path: "/resources/workstation/history", query: "from=2026-11-05T00%3A00%3A00Z&limit=100&step=5m&to=2026-11-05T09%3A30%3A00Z", response: `{"items":[],"nextCursor":""}`},
		{name: "events", args: []string{"events", "workstation"}, method: "GET", path: "/resources/workstation/events", query: "limit=100", response: `{"items":[]}`},
		{name: "capabilities", args: []string{"capabilities", "workstation"}, method: "GET", path: "/resources/workstation/capabilities", response: `{"items":[{"action":"power-on","available":false}]}`},
		{name: "maintenance status", args: []string{"maintenance", "workstation"}, method: "GET", path: "/resources/workstation/maintenance", response: `{"maintenance":null}`},
		{name: "maintenance preview", args: []string{"maintenance", "workstation", "preview"}, method: "GET", path: "/resources/workstation/maintenance/preview", response: `{"hash":"h","workloads":[]}`},
		{name: "operations", args: []string{"operations", "workstation"}, method: "GET", path: "/resources/workstation/operations", query: "limit=100", response: `{"items":[]}`},
		{name: "operation", args: []string{"operation", "workstation", "op-1"}, method: "GET", path: "/resources/workstation/operations/op-1", response: `{"id":"op-1","state":"indeterminate"}`},
		{name: "impact", args: []string{"impact", "workstation"}, method: "GET", path: "/resources/workstation/impact", response: `{"resource":"workstation"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != tt.method || r.URL.Path != "/api/v1"+tt.path || r.URL.RawQuery != tt.query {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("If-Match") != "" {
					t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			})
			s.cli("", append([]string{"resource"}, tt.args...)...).want(t, 0)
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
		})
	}
}

func TestResourceCLIWrites(t *testing.T) {
	confirmed := func(command ...string) []string {
		return append(command, "--reason", "scheduled work", "--confirm", "workstation", "--yes")
	}
	tests := []struct {
		name         string
		args         []string
		typ          string
		maintenance  string
		method, path string
		match        string
		body         map[string]any
		response     string
		stderr       []string
	}{
		{name: "maintenance enter", args: confirmed("maintenance", "workstation", "enter"), method: "POST", path: "/resources/workstation/maintenance",
			body: map[string]any{"reason": "scheduled work", "previewHash": previewHash}, response: `{"maintenance":{"state":"active"}}`, stderr: []string{"cache v3 running", "stops nothing"}},
		{name: "maintenance exit", args: confirmed("maintenance", "workstation", "exit"), method: "POST", path: "/resources/workstation/maintenance/exit",
			body: map[string]any{"reason": "scheduled work"}},
		{name: "reboot enters maintenance", args: append(confirmed("reboot", "workstation"), "--idempotency-key", "operator-key"), method: "POST", path: "/resources/workstation/operations",
			body: map[string]any{"action": "reboot", "idempotencyKey": "operator-key", "reason": "scheduled work", "confirm": "workstation"}, response: `{"id":"op-1","state":"observing","unresolved":true}`,
			stderr: []string{"enters maintenance", "cache v3 running", "version 7, idempotency key operator-key"}},
		{name: "reboot keeps maintenance", args: append(confirmed("reboot", "workstation"), "--idempotency-key", "operator-key"), maintenance: `{"reason":"window","enteredAt":"2026-11-05T01:00:00Z","actor":{"name":"admin"}}`,
			method: "POST", path: "/resources/workstation/operations",
			body:     map[string]any{"action": "reboot", "idempotencyKey": "operator-key", "reason": "scheduled work", "confirm": "workstation"},
			response: `{"id":"op-1","state":"observing","unresolved":true}`, stderr: []string{"by admin (window); the request keeps it"}},
		{name: "router reboot", args: append(confirmed("reboot", "workstation"), "--idempotency-key", "operator-key"), typ: "router", method: "POST", path: "/resources/workstation/operations",
			body: map[string]any{"action": "reboot", "idempotencyKey": "operator-key", "reason": "scheduled work", "confirm": "workstation"}, response: `{"id":"op-1","state":"observing","unresolved":true}`},
		{name: "shutdown", args: append(confirmed("shutdown", "workstation"), "--idempotency-key", "operator-key"), method: "POST", path: "/resources/workstation/operations",
			body: map[string]any{"action": "graceful-shutdown", "idempotencyKey": "operator-key", "reason": "scheduled work", "confirm": "workstation"}, response: `{"id":"op-1","state":"indeterminate","unresolved":true}`},
		{name: "resolve", args: confirmed("resolve", "workstation", "op-1"), method: "POST", path: "/resources/workstation/operations/op-1/resolve",
			body: map[string]any{"reason": "scheduled work"}, response: `{"id":"op-1","state":"indeterminate","unresolved":false}`},
		{name: "retire", args: confirmed("retire", "workstation"), method: "POST", path: "/resources/workstation/phase",
			body: map[string]any{"phase": "retired", "reason": "scheduled work", "version": float64(7)}, response: `{"name":"workstation","phase":"retired"}`, stderr: []string{"impact: no other managed resource"}},
		{name: "resume", args: confirmed("resume", "workstation"), method: "POST", path: "/resources/workstation/phase",
			body: map[string]any{"phase": "managed", "reason": "scheduled work", "version": float64(7)}, response: `{"name":"workstation","phase":"managed"}`},
		{name: "unmanage", args: confirmed("unmanage", "workstation"), method: "POST", path: "/resources/workstation/phase",
			body: map[string]any{"phase": "unmanaged", "reason": "scheduled work", "version": float64(7)}, response: `{"name":"workstation","phase":"unmanaged"}`},
		{name: "version override", args: append(confirmed("maintenance", "workstation", "exit"), "--version", "9"), method: "POST", path: "/resources/workstation/maintenance/exit",
			match: `"9"`, body: map[string]any{"reason": "scheduled work"}},
		{name: "retire acknowledging impact", args: append(confirmed("retire", "workstation"), "--acknowledge-impact"), method: "POST", path: "/resources/workstation/phase",
			body: map[string]any{"phase": "retired", "reason": "scheduled work", "version": float64(7), "acknowledgeImpact": true}, response: `{"name":"workstation","phase":"retired"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &site{typ: tt.typ, maintenance: tt.maintenance, write: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != "/api/v1"+tt.path {
					t.Errorf("request: %s %s", r.Method, r.URL)
				}
				match := tt.match
				if match == "" {
					match = `"7"`
				}
				if r.Header.Get("If-Match") != match {
					t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, tt.body) {
					t.Errorf("body: %v, want %v", body, tt.body)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing JSON content type")
				}
				if tt.response == "" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tt.response)
			}}
			got := st.serve(t).cli("", append([]string{"resource"}, tt.args...)...).want(t, 0)
			if st.writes.Load() != 1 {
				t.Fatalf("writes = %d", st.writes.Load())
			}
			for _, want := range tt.stderr {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
				}
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

// Without --idempotency-key the CLI chooses a key and prints it with the
// version, so that an ambiguous request can be retried exactly.
func TestResourceCLIGeneratesPrintedKey(t *testing.T) {
	var key string
	st := &site{write: func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		key, _ = body["idempotencyKey"].(string)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"op-1","state":"observing","unresolved":true}`)
	}}
	got := st.serve(t).cli("", "resource", "reboot", "workstation", "--reason", "kernel update", "--confirm", "workstation", "--yes").want(t, 0)
	if !idempotencyKeyPattern.MatchString(key) || !strings.HasPrefix(key, "cli-") || !strings.Contains(got.stderr, "version 7, idempotency key "+key) {
		t.Fatalf("key %q, stderr:\n%s", key, got.stderr)
	}
}

func TestResourceCLIAsksBeforeWrites(t *testing.T) {
	newSite := func() *site {
		return &site{write: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"op-1","state":"observing","unresolved":true}`)
		}}
	}
	args := []string{"resource", "reboot", "workstation", "--reason", "kernel update", "--confirm", "workstation"}

	// A script without --yes is refused after the preview; nothing is sent.
	st := newSite()
	got := st.serve(t).cli("y\n", args...).want(t, 2)
	if st.writes.Load() != 0 || !strings.Contains(got.stderr, "--yes") || !strings.Contains(got.stderr, "cache v3 running") {
		t.Fatalf("non-interactive: writes=%d %+v", st.writes.Load(), got)
	}
	// On a terminal anything but yes cancels.
	for _, answer := range []string{"n\n", "\n", ""} {
		st = newSite()
		got = st.serve(t).cliTTY(answer, args...).want(t, 1)
		if st.writes.Load() != 0 || !strings.Contains(got.stderr, "Proceed? [y/N]") || !strings.Contains(got.stderr, "nothing was sent") {
			t.Fatalf("answer %q: writes=%d %+v", answer, st.writes.Load(), got)
		}
	}
	st = newSite()
	st.serve(t).cliTTY("yes\n", args...).want(t, 0, "op-1")
	if st.writes.Load() != 1 {
		t.Fatalf("confirmed: writes=%d", st.writes.Load())
	}
	// Writes without a preview do not ask.
	st = &site{write: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }}
	st.serve(t).cli("", "resource", "maintenance", "workstation", "exit", "--reason", "done", "--confirm", "workstation").want(t, 0)
	if st.writes.Load() != 1 {
		t.Fatalf("exit: writes=%d", st.writes.Load())
	}
}

func TestResourceCLIRequiresImpactAcknowledgement(t *testing.T) {
	st := &site{impactRequired: true, write: func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["acknowledgeImpact"] != true {
			t.Errorf("body %v (%v)", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"op-1","state":"observing","unresolved":true}`)
	}}
	s := st.serve(t)
	args := []string{"resource", "reboot", "workstation", "--reason", "kernel update", "--confirm", "workstation", "--yes"}
	got := s.cli("", args...).want(t, 2)
	if st.writes.Load() != 0 || !strings.Contains(got.stderr, "impact: nas (controlPath workstation)") || !strings.Contains(got.stderr, "--acknowledge-impact") {
		t.Fatalf("unacknowledged: writes=%d %+v", st.writes.Load(), got)
	}
	s.cli("", append(args, "--acknowledge-impact")...).want(t, 0)
	if st.writes.Load() != 1 {
		t.Fatalf("acknowledged: writes=%d", st.writes.Load())
	}
}

func TestResourceCLIRejectsUnconfirmedWrites(t *testing.T) {
	var calls atomic.Int32
	s := syntheticServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	base := []string{"resource", "reboot", "workstation", "--reason", "scheduled work", "--confirm", "workstation", "--yes"}
	for _, remove := range []string{"--reason", "--confirm"} {
		t.Run(remove, func(t *testing.T) {
			var args []string
			for i := 0; i < len(base); i++ {
				if base[i] == remove {
					i++
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
		append(append([]string{}, base...), "--idempotency-key", "bad key"),
		append(append([]string{}, base...), "--version", "0"),
		append(append([]string{}, base...), "--acknowledge"),
		append(append([]string{}, base...), "--preview-hash", previewHash),
		{"resource", "operation", "workstation"},
		{"resource", "observe", "workstation", "extra"},
		{"resource", "events", "workstation", "--limit", "0"},
		{"resource", "events", "workstation", "--limit", "200"},
		{"resource", "events", "workstation", "-o", "table"},
		{"resource", "history", "workstation", "--from", "yesterday"},
		{"resource", "history", "workstation", "--to", "2026-11-05"},
		{"resource", "history", "workstation", "--from", "2026-11-05T10:00:00Z", "--to", "2026-11-05T10:00:00Z"},
		{"resource", "history", "workstation", "--step", "500ms"},
		{"resource", "history", "workstation", "--step", "5"},
		{"resource", "events", "workstation", "--step", "5m"},
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
	args := []string{"resource", "shutdown", "workstation", "--reason", "scheduled work", "--confirm", "workstation", "--yes", "--idempotency-key", "stable-key"}
	for _, status := range []int{401, 409, 422, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			st := &site{write: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"code":"precondition_failed","message":"synthetic rejection"}}`)
			}}
			got := st.serve(t).cli("", args...).want(t, 1)
			if st.writes.Load() != 1 || got.stdout != "" || !strings.Contains(got.stderr, "synthetic rejection") || !strings.Contains(got.stderr, "--idempotency-key stable-key --version 7") {
				t.Fatalf("error response: writes=%d %+v", st.writes.Load(), got)
			}
		})
	}
	st := &site{write: func(w http.ResponseWriter, r *http.Request) {
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
	}}
	got := st.serve(t).cli("", args...).want(t, 1)
	if st.writes.Load() != 1 || got.stdout != "" || !strings.Contains(got.stderr, "ambiguous") || !strings.Contains(got.stderr, "--idempotency-key stable-key --version 7") {
		t.Fatalf("ambiguous submission: writes=%d %+v", st.writes.Load(), got)
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
			st := &site{write: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, response)
			}}
			got := st.serve(t).cli("", "resource", "reboot", "workstation", "--reason", "scheduled work", "--confirm", "workstation", "--yes", "--idempotency-key", "stable-key").want(t, 1)
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
