package notify_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/nodes/notify"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/state"
)

type secrets map[string]string

func (s secrets) Get(name string) (secret.Value, error) {
	if v, ok := s[name]; ok {
		return secret.NewValue(v), nil
	}
	return secret.Value{}, fmt.Errorf("%w: %q", secret.ErrNotFound, name)
}

type request struct {
	Method, Path, Query string
	Header              http.Header
	Body                string
}

// stub records requests and answers like Discord, ntfy or a plain webhook.
type stub struct {
	*httptest.Server
	mu       sync.Mutex
	reqs     []request
	nextID   int
	gone     map[string]bool // message ids that answer PATCH with 404
	failWith int
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{gone: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reqs = append(s.reqs, request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)})
		if s.failWith != 0 {
			w.WriteHeader(s.failWith)
			return
		}
		switch {
		case r.Method == http.MethodPatch:
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if s.gone[id] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		case r.URL.Query().Get("wait") == "true" || strings.HasPrefix(r.URL.Path, "/ntfy"):
			s.nextID++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"m%d"}`, s.nextID)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) requests() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.reqs...)
}

func policy(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  concurrency: { mode: parallel, limit: 10 }\n  nodes:\n" + nodes
}

type env struct {
	h     *enginetest.Harness
	e     *engine.Engine
	stub  *stub
	state *state.Store
}

func setup(t *testing.T) env {
	t.Helper()
	h := enginetest.New(t)
	s := newStub(t)
	st := state.New(h.DB)
	notify.Register(h.Registry, notify.Deps{
		Client: s.Client(), State: st,
		Secrets: secrets{"discord-hook": s.URL + "/api/webhooks/42/SECRET-TOKEN", "ntfy-token": "tk_123", "bad-url": "not a url"},
	})
	return env{h: h, e: h.Open(), stub: s, state: st}
}

const discordPolicy = `
    - { id: start, type: trigger.manual, next: { out: [say] } }
    - id: say
      type: notify
      props: { target: discord, urlSecret: discord-hook, stateKey: deploy-msg, title: "{{ trigger.title }}", message: "{{ trigger.text }}" }
`

func (x env) say(t *testing.T, title, text string) engine.RunView {
	t.Helper()
	id := x.h.Start(x.e, "d", map[string]any{"title": title, "text": text})
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	return v
}

func TestDiscordEditsTheSameMessageAcrossRuns(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", discordPolicy))

	v := x.say(t, "Deploy", "started")
	n, _ := v.Node("say")
	out := n.Output.(map[string]any)
	if out["messageId"] != "m1" || out["edited"] != false || out["sent"] != true {
		t.Fatalf("first output = %v", out)
	}
	reqs := x.stub.requests()
	if len(reqs) != 1 || reqs[0].Method != "POST" || reqs[0].Path != "/api/webhooks/42/SECRET-TOKEN" || reqs[0].Query != "wait=true" {
		t.Fatalf("first request = %+v", reqs)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(reqs[0].Body), &body)
	if body["content"] != "**Deploy**\nstarted" {
		t.Fatalf("content = %v", body["content"])
	}
	if e, _ := x.state.Get(x.h.Ctx, "d", "deploy-msg"); !e.Found() || e.Value.(map[string]any)["messageId"] != "m1" {
		t.Fatalf("state = %+v", e)
	}

	v = x.say(t, "Deploy", "succeeded")
	n, _ = v.Node("say")
	out = n.Output.(map[string]any)
	if out["messageId"] != "m1" || out["edited"] != true {
		t.Fatalf("second output = %v", out)
	}
	reqs = x.stub.requests()
	if len(reqs) != 2 || reqs[1].Method != "PATCH" || reqs[1].Path != "/api/webhooks/42/SECRET-TOKEN/messages/m1" {
		t.Fatalf("second request = %+v", reqs[1:])
	}
	_ = json.Unmarshal([]byte(reqs[1].Body), &body)
	if body["content"] != "**Deploy**\nsucceeded" {
		t.Fatalf("edited content = %v", body["content"])
	}
	// The secret URL never appears in the run record.
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "SECRET-TOKEN") {
		t.Fatalf("run trace leaks the webhook token: %s", b)
	}
}

func TestDiscordDeletedMessageIsReposted(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", discordPolicy))
	x.say(t, "Deploy", "one")
	x.stub.mu.Lock()
	x.stub.gone["m1"] = true
	x.stub.mu.Unlock()

	v := x.say(t, "Deploy", "two")
	n, _ := v.Node("say")
	out := n.Output.(map[string]any)
	if out["messageId"] != "m2" || out["edited"] != false {
		t.Fatalf("output = %v", out)
	}
	methods := []string{}
	for _, r := range x.stub.requests() {
		methods = append(methods, r.Method)
	}
	if strings.Join(methods, ",") != "POST,PATCH,POST" {
		t.Fatalf("requests = %v", methods)
	}
	if e, _ := x.state.Get(x.h.Ctx, "d", "deploy-msg"); e.Value.(map[string]any)["messageId"] != "m2" {
		t.Fatalf("state = %+v", e)
	}
}

func TestDiscordWithoutStateKeyAlwaysPosts(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", `
    - { id: start, type: trigger.manual, next: { out: [say] } }
    - { id: say, type: notify, props: { target: discord, urlSecret: discord-hook, message: hello, username: LCM } }
`))
	id := x.h.Start(x.e, "d", nil)
	x.h.Wait(x.e, id)
	id = x.h.Start(x.e, "d", nil)
	x.h.Wait(x.e, id)
	reqs := x.stub.requests()
	if len(reqs) != 2 || reqs[0].Method != "POST" || reqs[1].Method != "POST" || reqs[0].Query != "" {
		t.Fatalf("requests = %+v", reqs)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(reqs[0].Body), &body)
	if body["content"] != "hello" || body["username"] != "LCM" {
		t.Fatalf("body = %v", body)
	}
}

func TestDiscordTruncatesLongMessages(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", discordPolicy))
	x.say(t, "T", strings.Repeat("é", 5000))
	var body map[string]any
	_ = json.Unmarshal([]byte(x.stub.requests()[0].Body), &body)
	if n := len([]rune(body["content"].(string))); n != 2000 {
		t.Fatalf("content has %d characters", n)
	}
}

func TestNtfyHeadersAndFallbackToNewMessages(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("n", `
    - { id: start, type: trigger.manual, next: { out: [say] } }
    - id: say
      type: notify
      props: { target: ntfy, url: "`+x.stub.URL+`/ntfy/alerts", title: "Disk\nfull", message: "98% used", priority: high, tags: [warning, disk], tokenSecret: ntfy-token }
`))
	id := x.h.Start(x.e, "n", nil)
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	r := x.stub.requests()[0]
	if r.Method != "POST" || r.Path != "/ntfy/alerts" || r.Body != "98% used" ||
		r.Header.Get("Title") != "Disk full" || r.Header.Get("Priority") != "high" || r.Header.Get("Tags") != "warning,disk" ||
		r.Header.Get("Authorization") != "Bearer tk_123" {
		t.Fatalf("request = %+v", r)
	}
	n, _ := v.Node("say")
	if n.Output.(map[string]any)["messageId"] != "m1" {
		t.Fatalf("output = %v", n.Output)
	}
}

func TestGenericWebhookBodies(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("w", `
    - { id: start, type: trigger.manual, next: { out: [a, b] } }
    - { id: a, type: notify, props: { target: webhook, url: "`+x.stub.URL+`/hook", title: T, message: hello } }
    - { id: b, type: notify, props: { target: webhook, url: "`+x.stub.URL+`/hook", message: ignored, payload: { text: "{{ trigger.text }}" } } }
`))
	id := x.h.Start(x.e, "w", map[string]any{"text": "custom"})
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	bodies := map[string]bool{}
	for _, r := range x.stub.requests() {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("missing idempotency key: %+v", r)
		}
		bodies[r.Body] = true
	}
	if !bodies[`{"text":"custom"}`] {
		t.Fatalf("custom payload not sent: %v", bodies)
	}
	var def map[string]any
	for b := range bodies {
		if strings.Contains(b, "message") {
			_ = json.Unmarshal([]byte(b), &def)
		}
	}
	if def["title"] != "T" || def["message"] != "hello" || def["policy"] != "w" || def["run"] != id {
		t.Fatalf("default body = %v", def)
	}
}

func TestNotifyFailureModes(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", discordPolicy))

	// Client errors fail the run without a retry, and never print the token.
	x.stub.mu.Lock()
	x.stub.failWith = http.StatusBadRequest
	x.stub.mu.Unlock()
	id := x.h.Start(x.e, "d", map[string]any{"title": "t", "text": "m"})
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunFailed || strings.Contains(v.Error, "SECRET-TOKEN") || !strings.Contains(v.Error, "400") {
		t.Fatalf("status=%s error=%q", v.Status, v.Error)
	}
	if n := len(x.stub.requests()); n != 1 {
		t.Fatalf("permanent failure was retried: %d requests", n)
	}
	if e, _ := x.state.Get(x.h.Ctx, "d", "deploy-msg"); e.Found() {
		t.Fatal("state written for a failed send")
	}
}

func TestNotifyDryRunSendsNothing(t *testing.T) {
	x := setup(t)
	x.h.Deploy(x.e, policy("d", discordPolicy))
	id, err := x.e.Start(x.h.Ctx, "d", "start", map[string]any{"title": "t", "text": "m"}, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunSucceeded || len(x.stub.requests()) != 0 {
		t.Fatalf("status=%s requests=%d", v.Status, len(x.stub.requests()))
	}
}

func TestNotifyValidationAndSecrets(t *testing.T) {
	x := setup(t)
	bad := map[string]string{
		`{ target: webhook, message: m }`:                                       "exactly one",
		`{ target: webhook, message: m, url: "http://x", urlSecret: s }`:        "exactly one",
		`{ target: webhook, message: " ", url: "http://x" }`:                    "message",
		`{ target: ntfy, message: m, url: "http://x", stateKey: k }`:            "only supported for discord",
		`{ target: discord, message: m, url: "http://x", stateKey: "bad key" }`: "stateKey",
		`{ target: discord, message: m, url: "http://x", payload: {} }`:         "payload",
		`{ target: webhook, message: m, url: "javascript:alert(1)" }`:           "url",
		`{ target: sms, message: m, url: "http://x" }`:                          "one of",
		`{ target: webhook, message: m, urlSecret: "../etc" }`:                  "urlSecret",
	}
	for props, want := range bad {
		_, err := x.e.Validate(x.h.Ctx, []byte(policy("v", "    - { id: start, type: trigger.manual, next: { out: [n] } }\n    - { id: n, type: notify, props: "+props+" }\n")))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", props, err, want)
		}
	}

	// Secrets that are missing or malformed fail permanently without detail.
	x.h.Deploy(x.e, policy("s", `
    - { id: start, type: trigger.manual, next: { out: [a, b] } }
    - { id: a, type: notify, props: { target: webhook, urlSecret: nope, message: m }, next: {} }
    - { id: b, type: notify, props: { target: webhook, urlSecret: bad-url, message: m } }
`))
	id := x.h.Start(x.e, "s", nil)
	v := x.h.Wait(x.e, id)
	if v.Status != engine.RunFailed || len(x.stub.requests()) != 0 {
		t.Fatalf("status=%s requests=%d", v.Status, len(x.stub.requests()))
	}
	for _, nid := range []string{"a", "b"} {
		if n, _ := v.Node(nid); n.Attempts > 1 {
			t.Errorf("node %s retried %d times", nid, n.Attempts)
		}
	}
}
