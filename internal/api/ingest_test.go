package api_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/trigger"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookPolicy(name, hook string, signed bool) string {
	props := "name: " + hook
	if signed {
		props += ", hmacSecret: hook-key"
	}
	return policyDoc(name, "    - { id: hook, type: trigger.webhook, props: { "+props+" }, next: { out: [echo] } }\n"+
		"    - { id: echo, type: data.set, props: { values: { body: \"{{ trigger.body }}\" } } }\n")
}

func TestWebhookAuthentication(t *testing.T) {
	x := newSite(t)
	writeSecret(t, x, "hook-key", "s3cret")
	x.call("PUT", v1+"/policies/signed", webhookPolicy("signed", "deploy-hook", true), yamlBody()).want(http.StatusCreated)
	x.call("PUT", v1+"/policies/open", webhookPolicy("open", "open-hook", false), yamlBody()).want(http.StatusCreated)
	runs := x.collect(events.TypeRun)
	body := []byte(`{"ref":"main"}`)
	sig := func(s string) opt { return header("X-Hub-Signature-256", s) }

	// A valid signature stands in for a token.
	r := x.anon("POST", v1+"/hooks/deploy-hook", body, sig(sign("s3cret", body))).want(http.StatusAccepted)
	ids := r.obj()["runs"].([]any)
	if len(ids) != 1 {
		t.Fatalf("runs = %v", ids)
	}
	view := x.waitRun(runs, ids[0].(string))
	if view["status"] != "succeeded" || view["policy"] != "signed" {
		t.Fatalf("run = %v", view)
	}
	if hdrs := view["event"].(map[string]any)["headers"].(map[string]any); hdrs["authorization"] != nil {
		t.Fatalf("authorization header stored: %v", hdrs)
	}

	// Without a valid signature or a token the request is unauthenticated.
	x.anon("POST", v1+"/hooks/deploy-hook", body).wantError(http.StatusUnauthorized, "unauthenticated")
	x.anon("POST", v1+"/hooks/deploy-hook", body, sig(sign("wrong", body))).wantError(http.StatusUnauthorized, "unauthenticated")
	x.anon("POST", v1+"/hooks/deploy-hook", body, sig(sign("s3cret", []byte("other")))).wantError(http.StatusUnauthorized, "unauthenticated")
	// A token does not waive the signature of a listener that requires one.
	x.call("POST", v1+"/hooks/deploy-hook", body, bearer(x.ingest)).wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("POST", v1+"/hooks/deploy-hook", body, bearer(x.ingest), sig(sign("s3cret", body))).want(http.StatusAccepted)

	// A listener without a secret needs a token (any scope).
	x.anon("POST", v1+"/hooks/open-hook", body).wantError(http.StatusUnauthorized, "unauthenticated")
	x.anon("POST", v1+"/hooks/open-hook", body, sig(sign("s3cret", body))).wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("POST", v1+"/hooks/open-hook", body, bearer(x.ingest)).want(http.StatusAccepted)
	x.call("POST", v1+"/hooks/open-hook", body, bearer(x.admin)).want(http.StatusAccepted)

	// Unknown hooks do not reveal themselves to anonymous callers.
	x.anon("POST", v1+"/hooks/nobody", body).wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("POST", v1+"/hooks/nobody", body, bearer(x.ingest)).wantError(http.StatusNotFound, "not_found")
	big := make([]byte, trigger.MaxWebhookBody+1)
	x.call("POST", v1+"/hooks/open-hook", big, bearer(x.ingest)).wantError(http.StatusRequestEntityTooLarge, "bad_request")
	x.call("GET", v1+"/hooks/open-hook", nil, bearer(x.ingest)).wantError(http.StatusNotFound, "not_found")
}

func TestGitHubPush(t *testing.T) {
	x := newSite(t, func(c *config.Config) { c.GitHubWebhookSecret = "gh-secret" })
	writeSecret(t, x, "gh-secret", "topsecret")
	const repo = "https://github.com/acme/app.git"
	x.git.set(repo, "main", "aaa")
	deploy := policyDoc("on-push", "    - { id: git, type: trigger.git, props: { repo: \""+repo+"\", branch: main, interval: 1h }, next: { out: [echo] } }\n"+
		"    - { id: echo, type: data.set, props: { values: { commit: \"{{ trigger.commit }}\" } } }\n")
	x.call("PUT", v1+"/policies/on-push", deploy, yamlBody()).want(http.StatusCreated)
	if err := x.app.Triggers.Sync(x.ctx); err != nil {
		t.Fatal(err)
	}
	runs := x.collect(events.TypeRun)

	push := func(ref, after string, deleted bool) []byte {
		b := `{"ref":"` + ref + `","after":"` + after + `","deleted":` + map[bool]string{true: "true", false: "false"}[deleted] +
			`,"repository":{"clone_url":"git@github.com:Acme/App.git","html_url":"https://github.com/acme/app"}}`
		return []byte(b)
	}
	post := func(body []byte, event, signature string, opts ...opt) reply {
		o := append([]opt{bearer(""), header("X-GitHub-Event", event)}, opts...)
		if signature != "" {
			o = append(o, header("X-Hub-Signature-256", signature))
		}
		return x.call("POST", v1+"/hooks/github", body, o...)
	}

	body := push("refs/heads/main", "bbb1234", false)
	d := post(body, "push", sign("topsecret", body)).want(http.StatusAccepted).obj()
	if d["matched"] != float64(1) || d["branch"] != "main" || d["commit"] != "bbb1234" {
		t.Fatalf("push = %v", d)
	}
	var started string
	runs.until("push run", func(e events.Event) bool {
		ev, ok := e.Data.(engine.Event)
		if ok && ev.Kind == engine.EventRunFinished {
			started = ev.RunID
		}
		return started != ""
	})
	if v := x.call("GET", v1+"/runs/"+started, nil).obj(); v["event"].(map[string]any)["commit"] != "bbb1234" {
		t.Fatalf("run event = %v", v["event"])
	}

	// Authentication.
	post(body, "push", "").wantError(http.StatusUnauthorized, "unauthenticated")
	post(body, "push", sign("wrong", body)).wantError(http.StatusUnauthorized, "unauthenticated")
	post(body, "push", "sha256=zz").wantError(http.StatusUnauthorized, "unauthenticated")
	post(body, "push", "", bearer(x.ingest)).want(http.StatusAccepted)

	// Events that start nothing are acknowledged.
	ping := []byte(`{"zen":"x"}`)
	if d := post(ping, "ping", sign("topsecret", ping)).want(http.StatusAccepted).obj(); d["ignored"] != true {
		t.Fatalf("ping = %v", d)
	}
	for _, b := range [][]byte{push("refs/tags/v1", "ccc", false), push("refs/heads/main", "ddd", true), push("refs/heads/main", strings.Repeat("0", 40), false)} {
		if d := post(b, "push", sign("topsecret", b)).want(http.StatusAccepted).obj(); d["ignored"] != true {
			t.Fatalf("ignored push = %v", d)
		}
	}
	other := push("refs/heads/dev", "eee1234", false)
	if d := post(other, "push", sign("topsecret", other)).want(http.StatusAccepted).obj(); d["matched"] != float64(0) {
		t.Fatalf("other branch = %v", d)
	}
	bad := []byte(`{not json`)
	post(bad, "push", sign("topsecret", bad)).wantError(http.StatusBadRequest, "bad_request")
}

func TestGitHubWithoutConfiguredSecretNeedsToken(t *testing.T) {
	x := newSite(t)
	body := []byte(`{"ref":"refs/heads/main","after":"abc1234","repository":{"clone_url":"https://github.com/a/b.git"}}`)
	h := header("X-GitHub-Event", "push")
	x.anon("POST", v1+"/hooks/github", body, h, header("X-Hub-Signature-256", sign("anything", body))).
		wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("POST", v1+"/hooks/github", body, h, bearer(x.ingest)).want(http.StatusAccepted)
}
