// Package notify provides the notify node: messages to a generic webhook,
// a Discord webhook, ntfy or a BotFleet konata notification relay,
// optionally editing an earlier message in place.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/httpcall"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/state"
)

// Targets.
const (
	TargetWebhook = "webhook"
	TargetDiscord = "discord"
	TargetNtfy    = "ntfy"
	TargetKonata  = "konata"
)

// Konata body limits, from the relay's API.
const (
	konataTitleLimit   = 256
	konataSummaryLimit = 500
	konataURLLimit     = 500
	konataKeyLimit     = 100
	maxStages          = 10
	stageLabelLimit    = 100
)

var (
	statuses    = []string{"running", "succeeded", "failed", "cancelled"}
	stageStates = []string{"pending", "running", "done", "failed", "skipped"}
)

// Message limits. Discord rejects more than 2000 characters of content.
const (
	discordLimit = 2000
	genericLimit = 8000
)

// Deps are the collaborators of notify.
type Deps struct {
	// Client defaults to a client that follows redirects only within one host.
	Client *http.Client
	// Secrets resolves urlSecret and tokenSecret.
	Secrets secret.Source
	// State holds the message ids behind stateKey; without it stateKey is
	// rejected at run time.
	State *state.Store
}

// Props configures notify.
type Props struct {
	Target      string   `json:"target" lcm:"required" enum:"webhook,discord,ntfy,konata" desc:"where to send"`
	URL         string   `json:"url" desc:"destination URL (konata: the route, such as http://127.0.0.1:4100/notifications/deploys); Discord webhook URLs contain a token, so prefer urlSecret"`
	URLSecret   string   `json:"urlSecret" desc:"secret name holding the destination URL"`
	Message     string   `json:"message" lcm:"required" desc:"message text"`
	Title       string   `json:"title" desc:"short heading"`
	StateKey    string   `json:"stateKey" desc:"Discord: remember the message id under this policy state key and edit that message on later runs; konata: nodes of one run with the same key edit one message"`
	Payload     any      `json:"payload" desc:"webhook: JSON body to send instead of the default {title, message, policy, run}"`
	Priority    string   `json:"priority" enum:"min,low,default,high,urgent" desc:"ntfy priority"`
	Tags        []string `json:"tags" desc:"ntfy tags"`
	Username    string   `json:"username" desc:"Discord: name shown for the message"`
	TokenSecret string   `json:"tokenSecret" desc:"secret name sent as a bearer token (webhook, ntfy, and konata where it is required)"`
	Optional    bool     `json:"optional" desc:"send nothing, and succeed, when neither url nor urlSecret is set; for destinations that a template parameter may leave empty"`
	Status      string   `json:"status" enum:"running,succeeded,failed,cancelled" desc:"overall state; konata colours the message by it (default running), other targets add it to the text"`
	Stages      []Stage  `json:"stages" desc:"progress steps shown with the message, at most 10"`
	Link        string   `json:"link" desc:"http or https URL the notification refers to, such as a commit"`
}

// Stage is one progress step of a notification.
type Stage struct {
	Label string `json:"label" lcm:"required" desc:"step name, at most 100 characters"`
	State string `json:"state" lcm:"required" enum:"pending,running,done,failed,skipped" desc:"step state"`
}

func validate(p Props) error {
	switch {
	case p.URL != "" && p.URLSecret != "":
		return errors.New("set exactly one of url and urlSecret, not both")
	case p.URL == "" && p.URLSecret == "" && !p.Optional:
		return errors.New("set exactly one of url and urlSecret")
	case p.URLSecret != "" && !secret.ValidName(p.URLSecret):
		return errors.New("urlSecret: invalid secret name")
	case p.TokenSecret != "" && !secret.ValidName(p.TokenSecret):
		return errors.New("tokenSecret: invalid secret name")
	case p.StateKey != "" && !state.ValidKey(p.StateKey):
		return fmt.Errorf("stateKey: invalid key %q", p.StateKey)
	case p.StateKey != "" && p.Target != TargetDiscord && p.Target != TargetKonata:
		return fmt.Errorf("stateKey: editing is only supported for discord and konata; %s always sends a new message", p.Target)
	case p.Target == TargetKonata && p.TokenSecret == "":
		return errors.New("tokenSecret: konata needs the relay's bearer key")
	case p.Status != "" && !slices.Contains(statuses, p.Status):
		return fmt.Errorf("status: must be one of %s", strings.Join(statuses, ", "))
	case len(p.Stages) > maxStages:
		return fmt.Errorf("stages: at most %d", maxStages)
	case p.Payload != nil && p.Target != TargetWebhook:
		return errors.New("payload: only used by the webhook target")
	case strings.TrimSpace(p.Message) == "":
		return errors.New("message: must not be empty")
	}
	for i, st := range p.Stages {
		if strings.TrimSpace(st.Label) == "" || utf8.RuneCountInString(st.Label) > stageLabelLimit {
			return fmt.Errorf("stages[%d].label: 1 to %d characters", i, stageLabelLimit)
		}
		if !slices.Contains(stageStates, st.State) {
			return fmt.Errorf("stages[%d].state: must be one of %s", i, strings.Join(stageStates, ", "))
		}
	}
	if p.URL != "" {
		if err := (httpcall.Spec{URL: p.URL}).Validate(); err != nil {
			return errors.New("url: want an absolute http or https URL")
		}
	}
	if p.Link != "" {
		if err := (httpcall.Spec{URL: p.Link}).Validate(); err != nil || len(p.Link) > konataURLLimit {
			return fmt.Errorf("link: want an absolute http or https URL of at most %d characters", konataURLLimit)
		}
	}
	return nil
}

// Register adds the notify node type to r.
func Register(r *node.Registry, d Deps) {
	node.Register(r, node.Spec[Props]{
		Type: "notify", Category: "notify", Effect: node.EffectWrite,
		Description: "Sends a notification to a webhook, Discord, ntfy or a konata relay. With stateKey, Discord messages are edited in place on later runs and konata messages within a run. Output: sent, target, messageId, edited.",
		Validate:    validate,
		Run: func(ctx context.Context, rc *node.RunContext, p Props) (node.Result, error) {
			return send(ctx, rc, d, p)
		},
	})
}

func send(ctx context.Context, rc *node.RunContext, d Deps, p Props) (node.Result, error) {
	if p.Optional && p.URL == "" && p.URLSecret == "" {
		return node.Result{Output: map[string]any{"sent": false, "target": p.Target, "skipped": "no destination configured"}}, nil
	}
	target, err := resolveURL(d, p)
	if err != nil {
		return node.Result{}, node.Permanent(err)
	}
	var out map[string]any
	switch p.Target {
	case TargetDiscord:
		out, err = sendDiscord(ctx, rc, d, p, target)
	case TargetNtfy:
		out, err = sendNtfy(ctx, rc, d, p, target)
	case TargetKonata:
		out, err = sendKonata(ctx, rc, d, p, target)
	default:
		out, err = sendWebhook(ctx, rc, d, p, target)
	}
	if err != nil {
		return node.Result{}, classify(err)
	}
	out["sent"], out["target"] = true, p.Target
	return node.Result{Output: out}, nil
}

func resolveURL(d Deps, p Props) (string, error) {
	if p.URL != "" {
		return p.URL, nil
	}
	if d.Secrets == nil {
		return "", errors.New("secrets are not configured")
	}
	v, err := d.Secrets.Get(p.URLSecret)
	if err != nil {
		return "", err
	}
	u := strings.TrimSpace(v.Reveal())
	if err := (httpcall.Spec{URL: u}).Validate(); err != nil {
		// The error would not contain the value, but say nothing about it.
		return "", fmt.Errorf("secret %q is not an absolute http or https URL", p.URLSecret)
	}
	return u, nil
}

// classify retries server errors and transport failures, not client errors
// or missing secrets.
func classify(err error) error {
	var se *httpcall.StatusError
	switch {
	case errors.As(err, &se) && !se.Retryable():
		return node.Permanent(err)
	case errors.Is(err, secret.ErrNotFound), errors.Is(err, secret.ErrInvalidName), errors.Is(err, errNoState):
		return node.Permanent(err)
	}
	return err
}

var errNoState = errors.New("stateKey needs the policy state store, which is not configured")

func (d Deps) do(ctx context.Context, rc *node.RunContext, s httpcall.Spec) (httpcall.Response, error) {
	c := d.Client
	if c == nil {
		c = httpcall.NewClient()
	}
	s.SecretURL = true
	return httpcall.Do(ctx, rc.Clock, c, d.Secrets, s)
}

func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit-1]) + "…"
}

// body is the message followed by the status, the stages and the link, for
// targets that only take text.
func body(p Props) string {
	var b strings.Builder
	b.WriteString(p.Message)
	if p.Status != "" {
		b.WriteString("\nStatus: " + p.Status)
	}
	for _, st := range p.Stages {
		b.WriteString("\n" + stageMarks[st.State] + " " + st.Label)
	}
	if p.Link != "" {
		b.WriteString("\n" + p.Link)
	}
	return b.String()
}

// stageMarks match the ones konata draws.
var stageMarks = map[string]string{"pending": "○", "running": "●", "done": "✓", "failed": "✗", "skipped": "–"}

func text(p Props, limit int) string {
	if p.Title == "" {
		return truncate(body(p), limit)
	}
	return truncate("**"+p.Title+"**\n"+body(p), limit)
}

func sendWebhook(ctx context.Context, rc *node.RunContext, d Deps, p Props, target string) (map[string]any, error) {
	body := p.Payload
	if body == nil {
		m := map[string]any{
			"title": p.Title, "message": truncate(p.Message, genericLimit), "policy": rc.Policy, "run": rc.RunID,
		}
		if p.Status != "" {
			m["status"] = p.Status
		}
		if len(p.Stages) > 0 {
			m["stages"] = p.Stages
		}
		if p.Link != "" {
			m["link"] = p.Link
		}
		body = m
	}
	resp, err := d.do(ctx, rc, httpcall.Spec{
		Method: http.MethodPost, URL: target, Body: body, BearerSecret: p.TokenSecret,
		Headers: map[string]string{"Idempotency-Key": rc.IdempotencyKey},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": int64(resp.Status), "edited": false}, nil
}

func sendNtfy(ctx context.Context, rc *node.RunContext, d Deps, p Props, target string) (map[string]any, error) {
	h := map[string]string{}
	if p.Title != "" {
		h["Title"] = headerSafe(p.Title)
	}
	if p.Priority != "" {
		h["Priority"] = p.Priority
	}
	if len(p.Tags) > 0 {
		h["Tags"] = headerSafe(strings.Join(p.Tags, ","))
	}
	resp, err := d.do(ctx, rc, httpcall.Spec{
		Method: http.MethodPost, URL: target, Body: truncate(body(p), genericLimit), Headers: h, BearerSecret: p.TokenSecret,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"status": int64(resp.Status), "edited": false}
	if id := jsonString(resp.Body, "id"); id != "" {
		out["messageId"] = id
	}
	return out, nil
}

// sendKonata PUTs the structured message to a konata notification route.
// The thread key names the message: konata posts it on the first PUT and
// edits it on later ones, so nodes of one run that share a stateKey update
// one message, and every run starts a new one.
func sendKonata(ctx context.Context, rc *node.RunContext, d Deps, p Props, target string) (map[string]any, error) {
	thread := p.StateKey
	if thread == "" {
		thread = rc.NodeID
	}
	title := p.Title
	if strings.TrimSpace(title) == "" {
		title = rc.Policy
	}
	status := p.Status
	if status == "" {
		status = "running"
	}
	stages := p.Stages
	if stages == nil {
		stages = []Stage{}
	}
	b := map[string]any{
		"title":      truncate(title, konataTitleLimit),
		"summary":    truncate(p.Message, konataSummaryLimit),
		"status":     status,
		"stages":     stages,
		"occurredAt": rc.Now().UTC().Format(time.RFC3339),
	}
	if p.Link != "" {
		b["url"] = p.Link
	}
	resp, err := d.do(ctx, rc, httpcall.Spec{
		Method: http.MethodPut, URL: strings.TrimRight(target, "/") + "/" + konataKey(rc.RunID+"."+thread),
		Body: b, BearerSecret: p.TokenSecret,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"status": int64(resp.Status), "edited": true}
	if m, ok := resp.Body.(map[string]any); ok {
		if created, ok := m["created"].(bool); ok {
			out["edited"] = !created
		}
	}
	if id := jsonString(resp.Body, "messageId"); id != "" {
		out["messageId"] = id
	}
	return out, nil
}

// konataKey maps s to the relay's key alphabet [A-Za-z0-9._-], at most 100
// characters; run ids come first, so distinct runs never share a key.
func konataKey(s string) string {
	k := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, s)
	if len(k) > konataKeyLimit {
		k = k[:konataKeyLimit]
	}
	return k
}

// headerSafe removes characters that cannot appear in an HTTP header value.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func sendDiscord(ctx context.Context, rc *node.RunContext, d Deps, p Props, target string) (map[string]any, error) {
	base, err := url.Parse(target)
	if err != nil {
		return nil, node.Permanent(errors.New("destination is not a valid URL"))
	}
	body := map[string]any{"content": text(p, discordLimit)}
	if p.Username != "" {
		body["username"] = truncate(p.Username, 80)
	}

	if p.StateKey != "" {
		if d.State == nil {
			return nil, errNoState
		}
		prev, err := d.State.Get(ctx, rc.Policy, p.StateKey)
		if err != nil {
			return nil, err
		}
		if id := messageID(prev.Value); id != "" {
			edit := *base
			edit.Path = strings.TrimRight(edit.Path, "/") + "/messages/" + url.PathEscape(id)
			resp, err := d.do(ctx, rc, httpcall.Spec{Method: http.MethodPatch, URL: edit.String(), Body: map[string]any{"content": body["content"]}})
			if err == nil {
				return map[string]any{"status": int64(resp.Status), "messageId": id, "edited": true}, nil
			}
			var se *httpcall.StatusError
			// The message was deleted by hand: post a fresh one instead.
			if !errors.As(err, &se) || se.Status != http.StatusNotFound {
				return nil, err
			}
		}
	}

	post := *base
	if p.StateKey != "" {
		// wait=true makes Discord return the created message, whose id we keep.
		q := post.Query()
		q.Set("wait", "true")
		post.RawQuery = q.Encode()
	}
	resp, err := d.do(ctx, rc, httpcall.Spec{Method: http.MethodPost, URL: post.String(), Body: body})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"status": int64(resp.Status), "edited": false}
	if p.StateKey != "" {
		id := jsonString(resp.Body, "id")
		if id == "" {
			return nil, node.Permanent(errors.New("discord did not return a message id; cannot remember the message"))
		}
		if _, err := d.State.Set(ctx, rc.Policy, p.StateKey, map[string]any{"messageId": id}); err != nil {
			return nil, err
		}
		out["messageId"] = id
	}
	return out, nil
}

func messageID(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["messageId"].(string); ok {
			return s
		}
	}
	return ""
}

func jsonString(body any, key string) string {
	if m, ok := body.(map[string]any); ok {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return ""
}
