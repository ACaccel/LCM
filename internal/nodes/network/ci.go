package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/httpcall"
	"github.com/ACaccel/lcm/internal/nodes/internal/poll"
)

// Ports of ci.status.
const (
	PortSuccess = "success"
	PortFailure = "failure"
	PortPending = "pending"
)

// ciPageSize and ciMaxPages bound the check runs read for one commit.
const (
	ciPageSize = 100
	ciMaxPages = 10
)

// CIProps configures ci.status.
type CIProps struct {
	Repo        string        `json:"repo" lcm:"required" desc:"GitHub repository as owner/name"`
	Commit      string        `json:"commit" lcm:"required" desc:"commit SHA (or a tag or branch name without slashes)"`
	Workflows   []string      `json:"workflows" desc:"workflow file names such as ci.yml; when set, the latest push run of each must have succeeded and other check runs are ignored"`
	TokenSecret string        `json:"tokenSecret" desc:"secret name of a GitHub token; needed for private repositories"`
	WaitFor     node.Duration `json:"waitFor" desc:"while checks are pending, keep polling for up to this long without holding a worker"`
	Interval    node.Duration `json:"interval" default:"30s" desc:"polling interval for waitFor"`
	Timeout     node.Duration `json:"timeout" default:"20s" desc:"timeout of each API call"`
}

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	commitRe = regexp.MustCompile(`^[A-Za-z0-9._-]{4,100}$`)
	// workflowRe accepts the file name of a workflow, not a path.
	workflowRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}\.ya?ml$`)
)

func validateCI(p CIProps) error {
	switch {
	case !repoRe.MatchString(p.Repo) || strings.Contains(p.Repo, ".."):
		return fmt.Errorf("repo: want owner/name, got %q", p.Repo)
	case !commitRe.MatchString(p.Commit) || strings.Contains(p.Commit, ".."):
		return fmt.Errorf("commit: invalid value %q", p.Commit)
	case p.WaitFor < 0 || p.Interval.D() < time.Second:
		return fmt.Errorf("waitFor must not be negative and interval must be at least 1s")
	}
	for i, w := range p.Workflows {
		if !workflowRe.MatchString(w) {
			return fmt.Errorf("workflows[%d]: want a workflow file name such as ci.yml, got %q", i, w)
		}
	}
	return nil
}

// checkRun is the part of a GitHub check run that matters here.
type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

// ciState summarizes check runs: any failed run means failure, otherwise
// any unfinished run (or no run at all, since CI may not have started yet)
// means pending, otherwise success.
func ciState(runs []checkRun) string {
	if len(runs) == 0 {
		return PortPending
	}
	pending := false
	for _, r := range runs {
		if r.Status != "completed" {
			pending = true
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return PortFailure
		}
	}
	if pending {
		return PortPending
	}
	return PortSuccess
}

func registerCI(r *node.Registry, d Deps) {
	node.Register(r, node.Spec[CIProps]{
		Type: "ci.status", Category: CategoryNetwork, Effect: node.EffectRead,
		Description: "Reads the GitHub Actions check runs of a commit, or with workflows the latest push run of each named workflow. Output: state (success, failure or pending) and runs. With waitFor it keeps polling while pending.",
		Ports:       []string{PortSuccess, PortFailure, PortPending},
		Validate:    validateCI,
		Run: func(ctx context.Context, rc *node.RunContext, p CIProps) (node.Result, error) {
			now := rc.Now()
			st, resumed := poll.Load(rc.Resumed)
			if !resumed {
				st = poll.Begin(now, p.WaitFor.D())
			}
			st.Polls++

			var (
				runs []checkRun
				err  error
			)
			if len(p.Workflows) > 0 {
				runs, err = fetchWorkflowRuns(ctx, rc, d, p)
			} else {
				runs, err = fetchCheckRuns(ctx, rc, d, p)
			}
			if err != nil {
				return node.Result{}, failure(err)
			}
			state := ciState(runs)
			if len(p.Workflows) > 0 {
				state = workflowState(runs, len(p.Workflows))
			}
			out := map[string]any{"state": state, "total": int64(len(runs)), "runs": runsOutput(runs)}
			if state == PortPending && p.WaitFor > 0 && !st.Expired(now) {
				at := st.NextWake(now, p.Interval.D())
				return node.Result{Suspend: &node.Suspend{ResumeAt: &at, Output: st.Output(map[string]any{"state": state})}}, nil
			}
			out["polls"] = int64(st.Polls)
			return node.Result{Port: state, Output: out}, nil
		},
	})
}

func fetchCheckRuns(ctx context.Context, rc *node.RunContext, d Deps, p CIProps) ([]checkRun, error) {
	var all []checkRun
	for page := 1; page <= ciMaxPages; page++ {
		u := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=%d&page=%d",
			strings.TrimRight(d.githubAPI(), "/"), p.Repo, url.PathEscape(p.Commit), ciPageSize, page)
		var body struct {
			Total int        `json:"total_count"`
			Runs  []checkRun `json:"check_runs"`
		}
		if err := getGitHubJSON(ctx, rc, d, p, u, &body); err != nil {
			return nil, err
		}
		all = append(all, body.Runs...)
		if len(all) >= body.Total || len(body.Runs) == 0 {
			break
		}
	}
	return all, nil
}

// workflowRun is the part of a GitHub Actions workflow run that matters here.
type workflowRun struct {
	checkRun
	Path      string    `json:"path"`
	RunNumber int64     `json:"run_number"`
	CreatedAt time.Time `json:"created_at"`
}

// fetchWorkflowRuns returns, for each requested workflow, its newest
// push-event run for the commit. A workflow without a run yet is reported as
// a queued run so that the overall state stays pending while CI starts.
func fetchWorkflowRuns(ctx context.Context, rc *node.RunContext, d Deps, p CIProps) ([]checkRun, error) {
	latest := make(map[string]workflowRun, len(p.Workflows))
	for page := 1; page <= ciMaxPages; page++ {
		u := fmt.Sprintf("%s/repos/%s/actions/runs?head_sha=%s&event=push&per_page=%d&page=%d",
			strings.TrimRight(d.githubAPI(), "/"), p.Repo, url.QueryEscape(p.Commit), ciPageSize, page)
		var body struct {
			Total int           `json:"total_count"`
			Runs  []workflowRun `json:"workflow_runs"`
		}
		if err := getGitHubJSON(ctx, rc, d, p, u, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Runs {
			file := path.Base(r.Path)
			if !slices.Contains(p.Workflows, file) {
				continue
			}
			if cur, ok := latest[file]; !ok || r.newerThan(cur) {
				r.Name = file
				latest[file] = r
			}
		}
		if page*ciPageSize >= body.Total || len(body.Runs) == 0 {
			break
		}
	}
	runs := make([]checkRun, 0, len(p.Workflows))
	for _, file := range p.Workflows {
		if r, ok := latest[file]; ok {
			runs = append(runs, r.checkRun)
			continue
		}
		runs = append(runs, checkRun{Name: file, Status: "queued"})
	}
	return runs, nil
}

func (r workflowRun) newerThan(o workflowRun) bool {
	if r.RunNumber != o.RunNumber {
		return r.RunNumber > o.RunNumber
	}
	return r.CreatedAt.After(o.CreatedAt)
}

// workflowState is ciState for workflow mode: success needs every named
// workflow to have a successful run, and a missing run keeps it pending.
func workflowState(runs []checkRun, want int) string {
	if len(runs) < want {
		return PortPending
	}
	return ciState(runs)
}

// getGitHubJSON reads one GitHub API response into dst.
func getGitHubJSON(ctx context.Context, rc *node.RunContext, d Deps, p CIProps, u string, dst any) error {
	resp, err := httpcall.Do(ctx, rc.Clock, d.client(), d.Secrets, httpcall.Spec{
		Method: "GET", URL: u, Timeout: p.Timeout.D(), BearerSecret: p.TokenSecret, MaxBytes: httpcall.MaxBytesLimit,
		Headers: map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"},
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(resp.Body)
	if err != nil || resp.Truncated {
		return node.Permanent(fmt.Errorf("unexpected GitHub response for %s", p.Repo))
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return node.Permanent(fmt.Errorf("unexpected GitHub response for %s: %w", p.Repo, err))
	}
	return nil
}

// maxRunsOutput keeps the node output well below the engine's value cap on
// repositories with hundreds of check runs.
const maxRunsOutput = 50

// runsOutput lists failed runs first, then unfinished ones, then the rest,
// so that what matters survives the cap.
func runsOutput(runs []checkRun) []any {
	rank := func(r checkRun) int {
		switch {
		case r.Status != "completed":
			return 1
		case r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped":
			return 2
		}
		return 0
	}
	sorted := slices.Clone(runs)
	slices.SortStableFunc(sorted, func(a, b checkRun) int { return rank(a) - rank(b) })
	sorted = sorted[:min(len(sorted), maxRunsOutput)]
	out := make([]any, len(sorted))
	for i, r := range sorted {
		out[i] = map[string]any{"name": r.Name, "status": r.Status, "conclusion": r.Conclusion, "url": r.URL}
	}
	return out
}
