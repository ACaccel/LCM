// Package trigger starts policy runs from the outside world: signals on the
// bus, git commits, cron schedules and webhooks. It defines the trigger node
// types (so that policies using them compile) and a Manager that watches
// the engine's deployed policies and runs one watcher per trigger node.
//
// Triggers call Engine.Start without an actor, as the engine documents for
// trigger subsystems. Progress that must survive restarts (the last commit
// seen, the last cron fire time) is kept in the trigger_state table.
package trigger

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/signal"
)

// Trigger node types.
const (
	TypeSignal  = "trigger.signal"
	TypeGit     = "trigger.git"
	TypeCron    = "trigger.cron"
	TypeWebhook = "trigger.webhook"
)

// Limits.
const (
	// MinGitInterval keeps a misconfigured policy from hammering a remote.
	MinGitInterval = 5 * time.Second
	// MaxCatchUp caps the runs started by catchUp: all.
	MaxCatchUp = 100
)

// SignalProps configures trigger.signal. Within one field any value may
// match; all given fields must match.
type SignalProps struct {
	Kinds      []string          `json:"kinds" desc:"signal kinds: check.changed, health.changed, lifecycle.changed, alert"`
	Services   []string          `json:"services" desc:"service names"`
	Labels     map[string]string `json:"labels" desc:"signal labels that must all be present with these values"`
	Severities []string          `json:"severities" desc:"info, warning or critical"`
	Statuses   []string          `json:"statuses" desc:"resulting status: data.status of checks and alerts, data.health of health changes, data.state of lifecycle changes"`
}

// GitProps configures trigger.git.
type GitProps struct {
	Repo        string        `json:"repo" lcm:"required" desc:"repository URL or local path"`
	Branch      string        `json:"branch" default:"main" desc:"branch to watch"`
	Interval    node.Duration `json:"interval" default:"1m" desc:"how often to poll the remote"`
	FireOnFirst bool          `json:"fireOnFirst" desc:"start a run for the head commit seen first, instead of only recording it"`
}

// CronProps configures trigger.cron.
type CronProps struct {
	Schedule     string `json:"schedule" lcm:"required" desc:"five-field cron expression, e.g. 0 3 * * *"`
	Timezone     string `json:"timezone" default:"UTC" desc:"IANA zone such as Asia/Taipei"`
	Calendar     string `json:"calendar" desc:"data file listing dates (YYYY-MM-DD per line)"`
	CalendarMode string `json:"calendarMode" enum:"skip,only" default:"skip" desc:"skip: do not fire on listed dates; only: fire only on listed dates"`
	CatchUp      string `json:"catchUp" enum:"skip,once,all" default:"skip" desc:"fires missed while LCM was down: skip, run the latest once, or run each (at most 100)"`
}

// WebhookProps configures trigger.webhook.
type WebhookProps struct {
	Name       string `json:"name" lcm:"required" desc:"name in the webhook URL"`
	HMACSecret string `json:"hmacSecret" desc:"secret name; when set the body must carry a valid HMAC-SHA256 signature"`
	HMACHeader string `json:"hmacHeader" default:"X-Hub-Signature-256" desc:"header carrying the hex signature, optionally prefixed sha256="`
}

var (
	branchRe  = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,200}$`)
	hookNameR = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	headerRe  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,100}$")

	signalKinds = []string{string(signal.KindCheck), string(signal.KindHealth), string(signal.KindLifecycle), string(signal.KindAlert)}
	severities  = []string{signal.SeverityInfo, signal.SeverityWarning, signal.SeverityCritical}
)

// Register adds the trigger node types to r.
func Register(r *node.Registry) {
	node.Register(r, node.Spec[SignalProps]{
		Type: TypeSignal, Category: node.CategoryTrigger,
		Description: "Starts a run when a matching signal (check, health, lifecycle or alert) is published. The signal is the trigger event.",
		Validate:    validateSignal,
	})
	node.Register(r, node.Spec[GitProps]{
		Type: TypeGit, Category: node.CategoryTrigger,
		Description: "Starts a run when a branch of a git remote gets a new commit. Event: repo, branch, commit, previous.",
		Validate:    validateGit,
	})
	node.Register(r, node.Spec[CronProps]{
		Type: TypeCron, Category: node.CategoryTrigger,
		Description: "Starts a run on a cron schedule in a time zone, optionally restricted by a date calendar. Event: scheduled, fired, catchUp.",
		Validate:    validateCron,
	})
	node.Register(r, node.Spec[WebhookProps]{
		Type: TypeWebhook, Category: node.CategoryTrigger,
		Description: "Starts a run when a request arrives at the named webhook. Event: name, headers, body.",
		Validate:    validateWebhook,
	})
}

func validateSignal(p SignalProps) error {
	if len(p.Kinds)+len(p.Services)+len(p.Labels)+len(p.Severities)+len(p.Statuses) == 0 {
		return fmt.Errorf("at least one filter is required; an unfiltered signal trigger would start a run for every signal")
	}
	if err := oneOf("kinds", p.Kinds, signalKinds); err != nil {
		return err
	}
	if err := oneOf("severities", p.Severities, severities); err != nil {
		return err
	}
	for name, list := range map[string][]string{"services": p.Services, "statuses": p.Statuses} {
		if slices.Contains(list, "") {
			return fmt.Errorf("%s: empty value", name)
		}
	}
	return nil
}

func oneOf(field string, got, allowed []string) error {
	for _, v := range got {
		if !slices.Contains(allowed, v) {
			return fmt.Errorf("%s: %q is not one of %s", field, v, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func validateGit(p GitProps) error {
	switch {
	case strings.HasPrefix(p.Repo, "-") || strings.ContainsAny(p.Repo, "\x00\r\n"):
		return fmt.Errorf("repo: invalid value")
	case !branchRe.MatchString(p.Branch) || strings.HasPrefix(p.Branch, "-") || strings.Contains(p.Branch, ".."):
		return fmt.Errorf("branch: invalid branch name %q", p.Branch)
	case p.Interval.D() < MinGitInterval:
		return fmt.Errorf("interval: %s is shorter than the minimum %s", p.Interval, MinGitInterval)
	}
	return nil
}

func validateCron(p CronProps) error {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: unknown zone %q", p.Timezone)
	}
	if _, err := ParseSchedule(p.Schedule, loc); err != nil {
		return fmt.Errorf("schedule: %w", err)
	}
	return nil
}

func validateWebhook(p WebhookProps) error {
	if !hookNameR.MatchString(p.Name) {
		return fmt.Errorf("name: %q must be 1 to 64 letters, digits, . _ -", p.Name)
	}
	if !headerRe.MatchString(p.HMACHeader) {
		return fmt.Errorf("hmacHeader: invalid header name %q", p.HMACHeader)
	}
	return nil
}
