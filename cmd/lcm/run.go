package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/ACaccel/lcm/internal/client"
)

// defaultPoll is how often --wait asks the server for progress.
const defaultPoll = time.Second

// pollInterval returns the wait polling interval.
func (a *app) pollInterval() time.Duration {
	if a.poll > 0 {
		return a.poll
	}
	return defaultPoll
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *app) runPolicy(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("run")
	trigger := fs.String("trigger", "", "id of the trigger node to start (default: the policy's only trigger.manual node)")
	event := fs.String("event", "", "JSON event handed to the trigger node")
	dry := fs.Bool("dry-run", false, "run without effects: nodes that act only report what they would do")
	wait := fs.Bool("wait", false, "wait for the run to finish; exits with status 1 when it did not succeed")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("run: expected exactly one policy name")
	}
	body := map[string]any{}
	if *trigger != "" {
		body["trigger"] = *trigger
	}
	if *event != "" {
		var ev any
		if err := json.Unmarshal([]byte(*event), &ev); err != nil {
			return usagef("run: --event is not valid JSON: %v", err)
		}
		body["event"] = ev
	}
	if *dry {
		body["mode"] = "dry"
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	var started struct {
		RunID   string `json:"runId"`
		Trigger string `json:"trigger"`
		Mode    string `json:"mode"`
	}
	if err := cl.JSON(ctx, "POST", client.Path("policies", pos[0], "runs"), nil, body, &started); err != nil {
		return apiError(err)
	}
	_, _ = fmt.Fprintf(a.stdout, "run %s started (%s, trigger %s, %s)\n", started.RunID, pos[0], started.Trigger, started.Mode)
	if !*wait {
		return nil
	}
	return a.waitRun(ctx, cl, started.RunID)
}

// terminalRun reports whether a run status is final.
func terminalRun(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "interrupted", "dropped":
		return true
	}
	return false
}

func (a *app) waitRun(ctx context.Context, cl *client.Client, id string) error {
	last := ""
	for {
		var run struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Nodes  []struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"nodes"`
		}
		if err := cl.JSON(ctx, "GET", client.Path("runs", id), nil, nil, &run); err != nil {
			return apiError(err)
		}
		if run.Status != last {
			_, _ = fmt.Fprintf(a.stdout, "run %s %s\n", id, run.Status)
			last = run.Status
		}
		if terminalRun(run.Status) {
			for _, n := range run.Nodes {
				line := fmt.Sprintf("  %-20s %-24s %s", n.ID, n.Type, n.Status)
				if n.Error != "" {
					line += ": " + n.Error
				}
				_, _ = fmt.Fprintln(a.stdout, line)
			}
			if run.Status != "succeeded" {
				msg := fmt.Sprintf("run %s %s", id, run.Status)
				if run.Error != "" {
					msg += ": " + run.Error
				}
				return fmt.Errorf("%s", msg)
			}
			return nil
		}
		if err := sleep(ctx, a.pollInterval()); err != nil {
			return fmt.Errorf("stopped waiting for run %s: %w", id, err)
		}
	}
}

func (a *app) action(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("action")
	version := fs.String("version", "", "commit or tag to deploy (deploy only)")
	job := fs.String("job", "", "name of the job to run (run-job only)")
	dry := fs.Bool("dry-run", false, "show the plan without performing the action")
	wait := fs.Bool("wait", false, "wait for the action to finish; exits with status 1 when it failed")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("action: expected <service> <action>")
	}
	body := map[string]any{}
	if *version != "" {
		body["version"] = *version
	}
	if *job != "" {
		body["job"] = *job
	}
	q := url.Values{}
	if *dry {
		q.Set("dryRun", "true")
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	svc, act := pos[0], pos[1]
	path := client.Path("services", svc, "actions", act)
	if *dry {
		var plan struct {
			From  string   `json:"from"`
			To    string   `json:"to"`
			Steps []string `json:"steps"`
		}
		if err := cl.JSON(ctx, "POST", path, q, body, &plan); err != nil {
			return apiError(err)
		}
		_, _ = fmt.Fprintf(a.stdout, "plan: %s %s (%s -> %s)\n", act, svc, plan.From, plan.To)
		for i, s := range plan.Steps {
			_, _ = fmt.Fprintf(a.stdout, "  %d. %s\n", i+1, s)
		}
		return nil
	}
	var started struct {
		ActionID string `json:"actionId"`
	}
	if err := cl.JSON(ctx, "POST", path, nil, body, &started); err != nil {
		return apiError(err)
	}
	_, _ = fmt.Fprintf(a.stdout, "action %s started (%s %s)\n", started.ActionID, act, svc)
	if !*wait {
		return nil
	}
	return a.waitAction(ctx, cl, started.ActionID)
}

func (a *app) waitAction(ctx context.Context, cl *client.Client, id string) error {
	for {
		var act struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Result *struct {
				From            string `json:"from"`
				To              string `json:"to"`
				Version         string `json:"version"`
				PreviousVersion string `json:"previousVersion"`
				DurationMs      int64  `json:"durationMs"`
			} `json:"result"`
		}
		if err := cl.JSON(ctx, "GET", client.Path("actions", id), nil, nil, &act); err != nil {
			return apiError(err)
		}
		switch act.Status {
		case "succeeded":
			line := fmt.Sprintf("action %s succeeded", id)
			if r := act.Result; r != nil {
				line += fmt.Sprintf(" (%s -> %s", r.From, r.To)
				if r.Version != "" {
					line += ", version " + r.Version
				}
				line += fmt.Sprintf(", %s)", (time.Duration(r.DurationMs) * time.Millisecond).Round(time.Millisecond))
			}
			_, err := fmt.Fprintln(a.stdout, line)
			return err
		case "failed":
			return fmt.Errorf("action %s failed: %s", id, act.Error)
		}
		if err := sleep(ctx, a.pollInterval()); err != nil {
			return fmt.Errorf("stopped waiting for action %s: %w", id, err)
		}
	}
}
