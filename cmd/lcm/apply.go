package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ACaccel/lcm/internal/client"
	"github.com/ACaccel/lcm/internal/configsync"
)

// applyResult is a document result of POST /apply.
type applyResult struct {
	Index    int            `json:"index"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	Action   string         `json:"action"`
	Version  int            `json:"version"`
	Diff     string         `json:"diff"`
	Reason   string         `json:"reason"`
	Issues   []issueMessage `json:"issues"`
	Warnings []issueMessage `json:"warnings"`
	Conflict bool           `json:"conflict"`
	Drifted  bool           `json:"drifted"`
	Path     string         `json:"path"`
}

type issueMessage struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (i issueMessage) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

func (a *app) apply(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("apply")
	var files multiFlag
	fs.Var(&files, "f", "file or directory of documents to apply; repeatable, \"-\" reads standard input")
	dry := fs.Bool("dry-run", false, "validate and show the changes without applying them")
	force := fs.Bool("force", false, "also apply resources that a config source owns (they stay owned and show as drifted)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("apply: unexpected argument %q", pos[0])
	}
	if len(files) == 0 {
		return usagef("apply: at least one -f <file|dir> is required")
	}
	docs, err := a.readDocuments(files)
	if err != nil {
		return err
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *dry {
		q.Set("dryRun", "true")
	}
	if *force {
		q.Set("force", "true")
	}
	var out struct {
		Items []applyResult `json:"items"`
	}
	resp, err := cl.Do(ctx, client.Request{Method: "POST", Path: "/apply", Query: q, Body: joinDocuments(docs), ContentType: "application/yaml"})
	if err != nil {
		return apiError(err)
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	counts := map[string]int{}
	problems := 0
	for _, r := range out.Items {
		if r.Index >= 0 && r.Index < len(docs) {
			r.Path = docs[r.Index].Path
		}
		a.printResult(r)
		counts[r.Action]++
		if r.Action == "failed" || r.Action == "skipped" {
			problems++
		}
	}
	mode := ""
	if *dry {
		mode = " (dry run)"
	}
	var parts []string
	for _, act := range []string{"create", "update", "unchanged", "skipped", "failed"} {
		if counts[act] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[act], act))
		}
	}
	_, _ = fmt.Fprintf(a.stdout, "%d documents: %s%s\n", len(out.Items), strings.Join(parts, ", "), mode)
	if problems > 0 {
		return problemError{fmt.Sprintf("%d of %d documents were not applied", problems, len(out.Items))}
	}
	return nil
}

// printResult writes one document result with its diff and problems.
func (a *app) printResult(r applyResult) {
	name := r.Kind + "/" + r.Name
	if r.Name == "" {
		name = "(unreadable document)"
	}
	_, _ = fmt.Fprintf(a.stdout, "%-9s %s  %s\n", r.Action, name, r.Path)
	if r.Reason != "" {
		_, _ = fmt.Fprintf(a.stdout, "  %s\n", r.Reason)
	}
	for _, is := range r.Issues {
		_, _ = fmt.Fprintf(a.stdout, "  error: %s\n", is)
	}
	for _, w := range r.Warnings {
		_, _ = fmt.Fprintf(a.stdout, "  warning: %s\n", w)
	}
	for _, l := range lines(r.Diff) {
		_, _ = fmt.Fprintf(a.stdout, "    %s\n", l)
	}
}

func (a *app) export(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("export")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("export: expected at most one argument, kind or kind/name")
	}
	q := url.Values{}
	if len(pos) == 1 {
		kind, name, _ := strings.Cut(pos[0], "/")
		if _, err := configsync.ParseKind(kind); err != nil {
			return usagef("export: %v (got %q)", err, kind)
		}
		q.Set("kind", kind)
		if name != "" {
			q.Set("name", name)
		}
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	resp, err := cl.Do(ctx, client.Request{Method: "GET", Path: "/export", Query: q})
	if err != nil {
		return apiError(err)
	}
	_, err = a.stdout.Write(resp.Body)
	return err
}

func (a *app) validate(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("validate")
	var files multiFlag
	fs.Var(&files, "f", "file or directory of documents to validate; repeatable, \"-\" reads standard input")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("validate: unexpected argument %q", pos[0])
	}
	if len(files) == 0 {
		return usagef("validate: at least one -f <file|dir> is required")
	}
	docs, err := a.readDocuments(files)
	if err != nil {
		return err
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}
	invalid := 0
	for _, d := range docs {
		var v struct {
			Kind     string         `json:"kind"`
			Name     string         `json:"name"`
			Valid    bool           `json:"valid"`
			Issues   []issueMessage `json:"issues"`
			Warnings []issueMessage `json:"warnings"`
		}
		resp, err := cl.Do(ctx, client.Request{Method: "POST", Path: "/validate", Body: d.Data, ContentType: "application/yaml"})
		if err != nil {
			return apiError(err)
		}
		if err := json.Unmarshal(resp.Body, &v); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		label := v.Kind + "/" + v.Name
		if v.Name == "" {
			label = "(unreadable document)"
		}
		status := "valid"
		if !v.Valid {
			status, invalid = "invalid", invalid+1
		}
		_, _ = fmt.Fprintf(a.stdout, "%-9s %s  %s\n", status, label, d.Path)
		for _, is := range v.Issues {
			_, _ = fmt.Fprintf(a.stdout, "  error: %s\n", is)
		}
		for _, w := range v.Warnings {
			_, _ = fmt.Fprintf(a.stdout, "  warning: %s\n", w)
		}
	}
	_, _ = fmt.Fprintf(a.stdout, "%d documents, %d valid\n", len(docs), len(docs)-invalid)
	if invalid > 0 {
		return problemError{fmt.Sprintf("%d of %d documents are invalid", invalid, len(docs))}
	}
	return nil
}
