package main

import (
	"context"
	"encoding/json"
	"fmt"
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
