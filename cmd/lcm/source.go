package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ACaccel/lcm/internal/client"
)

const sourceUsage = `Usage: lcm source <command> [args] [flags]

Commands:
  add --name <name> --repo <url> [--branch b] [--path p] [--interval d] [--enabled=false]
                                Register a config source (a Git repository LCM syncs)
  delete <name>                 Remove a source and its clone; the documents it owned
                                keep their content and are released
  sync <name> [--wait]          Request a sync now; --wait follows it and exits with
                                status 1 when it failed
  release <resource|policy|module> <name>
                                End the Git ownership of one document; it keeps its
                                content and the sync no longer touches it

Without --branch, --path and --interval the server applies its defaults.
`

type sourceFlags struct {
	name, repo, branch, path, interval string
	enabled, wait                      bool
}

func (a *app) source(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("source")
	fs.Usage = func() { _, _ = io.WriteString(a.stderr, sourceUsage); fs.PrintDefaults() }
	var f sourceFlags
	fs.StringVar(&f.name, "name", "", "source name (add)")
	fs.StringVar(&f.repo, "repo", "", "repository URL or path (add)")
	fs.StringVar(&f.branch, "branch", "", "branch to follow (add)")
	fs.StringVar(&f.path, "path", "", "directory of the repository holding the documents (add)")
	fs.StringVar(&f.interval, "interval", "", "sync interval such as 1m, from 10s to 24h (add)")
	fs.BoolVar(&f.enabled, "enabled", true, "sync the source periodically (add)")
	fs.BoolVar(&f.wait, "wait", false, "wait for the sync to finish (sync)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("source: expected add, delete, sync or release")
	}
	cmd, rest := pos[0], pos[1:]
	switch cmd {
	case "add":
		if len(rest) != 0 || f.name == "" || f.repo == "" {
			return usagef("source add: expected --name and --repo")
		}
		rest = []string{f.name}
	case "delete", "sync":
		if len(rest) != 1 {
			return usagef("source %s: expected exactly one source name", cmd)
		}
	case "release":
		if len(rest) != 2 {
			return usagef("source release: expected <resource|policy|module> <name>")
		}
		switch rest[0] {
		case "resource", "policy", "module":
		default:
			return usagef("source release: kind must be resource, policy or module")
		}
	default:
		return usagef("source: unknown command %q; expected add, delete, sync or release", cmd)
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	var done string
	switch cmd {
	case "add":
		err = sourceAdd(ctx, cl, f)
	case "delete":
		err = cl.JSON(ctx, "DELETE", client.Path("config", "sources", rest[0]), nil, nil, nil)
	case "release":
		err = cl.JSON(ctx, "DELETE", client.Path("config", "ownership", rest[0], rest[1]), nil, nil, nil)
	case "sync":
		done, err = a.sourceSync(ctx, cl, rest[0], f.wait)
	}
	if err != nil {
		return apiError(err)
	}
	if done == "" {
		done = "request accepted"
	}
	_, err = fmt.Fprintf(a.stdout, "source %s %s: %s\n", cmd, strings.Join(rest, "/"), done)
	return err
}

func sourceAdd(ctx context.Context, cl *client.Client, f sourceFlags) error {
	body := map[string]any{"name": f.name, "repo": f.repo, "enabled": f.enabled}
	for k, v := range map[string]string{"branch": f.branch, "path": f.path, "interval": f.interval} {
		if v != "" {
			body[k] = v
		}
	}
	return cl.JSON(ctx, "POST", "/config/sources", nil, body, nil)
}

// sourceView is the part of a source that a sync reports.
type sourceView struct {
	Syncing    bool   `json:"syncing"`
	LastCommit string `json:"lastCommit"`
	LastError  string `json:"lastError"`
}

// sourceSync requests a sync. With wait it follows the sync and returns the
// outcome line, or an error when the sync failed.
func (a *app) sourceSync(ctx context.Context, cl *client.Client, name string, wait bool) (string, error) {
	var src sourceView
	if err := cl.JSON(ctx, "POST", client.Path("config", "sources", name, "sync"), nil, nil, &src); err != nil {
		return "", err
	}
	if !wait {
		return "", nil
	}
	for src.Syncing {
		if err := sleep(ctx, a.pollInterval()); err != nil {
			return "", fmt.Errorf("stopped waiting for source %s: %w", name, err)
		}
		src = sourceView{}
		if err := cl.JSON(ctx, "GET", client.Path("config", "sources", name), nil, nil, &src); err != nil {
			return "", err
		}
	}
	if src.LastError != "" {
		return "", errors.New("source " + name + " sync failed: " + src.LastError)
	}
	return "synced " + short(src.LastCommit), nil
}
