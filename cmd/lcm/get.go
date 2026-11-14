package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/internal/client"
)

// getKind describes how `lcm get` lists and shows one kind of object.
type getKind struct {
	// list is the collection path; show returns the path of one item, or ""
	// when the API has no endpoint for it (the list is filtered instead).
	list    string
	show    func(name string) string
	key     string
	header  []string
	row     func(m map[string]any) []string
	perPage int
	// typed lists accept the --type filter.
	typed bool
	// parent, when set, makes the name argument required and selects the
	// resource whose items are listed; it returns the collection path.
	parent func(name string) string
}

var getKinds = map[string]getKind{
	"resources": {
		list: "/resources", show: func(n string) string { return client.Path("resources", n) }, key: "name", typed: true,
		header: []string{"NAME", "TYPE", "ADAPTER", "PHASE", "STATE", "HEALTH", "VERSION"},
		row: func(m map[string]any) []string {
			return []string{field(m, "name"), field(m, "type"), field(m, "adapter"), field(m, "phase"), field(m, "state"), health(m), field(m, "version")}
		},
	},
	"policies": {
		list: "/policies", show: func(n string) string { return client.Path("policies", n) }, key: "name",
		header: []string{"NAME", "VERSION", "DEPLOYED", "PAUSED", "TRIGGERS", "LAST RUN", "MANAGED BY"},
		row: func(m map[string]any) []string {
			dep := "no"
			if v := field(m, "deployedVersion"); v != "-" {
				dep = "v" + v
			}
			return []string{field(m, "name"), field(m, "latestVersion"), dep, field(m, "paused"), triggers(m), field(m, "lastRun.status"), managed(m)}
		},
	},
	"runs": {
		list: "/runs", show: func(n string) string { return client.Path("runs", n) }, key: "id", perPage: 50,
		header: []string{"ID", "POLICY", "VERSION", "STATUS", "MODE", "CREATED", "ERROR"},
		row: func(m map[string]any) []string {
			return []string{field(m, "id"), field(m, "policy"), field(m, "version"), field(m, "status"), field(m, "mode"), when(m, "createdAt"), field(m, "error")}
		},
	},
	"locks": {
		list: "/locks", key: "name",
		header: []string{"NAME", "OWNER", "ACQUIRED", "EXPIRES"},
		row: func(m map[string]any) []string {
			return []string{field(m, "name"), field(m, "owner"), when(m, "acquiredAt"), when(m, "expiresAt")}
		},
	},
	"connections": {
		list: "/connections", key: "name",
		header: []string{"NAME", "TYPE", "ALLOW ACTIONS", "ALLOW RESOURCES", "USED BY"},
		row: func(m map[string]any) []string {
			return []string{field(m, "name"), field(m, "type"), list(m, "allowActions", "-"), list(m, "allowResources", "any"), list(m, "resources", "-")}
		},
	},
	"backups": {
		parent: func(n string) string { return client.Path("resources", n, "backups") }, key: "backupId",
		header: []string{"BACKUP ID", "KIND", "STARTED", "OUTCOME", "RESTORABLE", "SIZE"},
		row: func(m map[string]any) []string {
			return []string{field(m, "backupId"), field(m, "kind"), when(m, "startedAt"), field(m, "outcome"), field(m, "restorable"), byteSize(m, "size")}
		},
	},
	"sources": {
		list: "/config/sources", show: func(n string) string { return client.Path("config", "sources", n) }, key: "name",
		header: []string{"NAME", "REPO", "BRANCH", "PATH", "INTERVAL", "ENABLED", "LAST SYNC", "COMMIT", "ERROR"},
		row: func(m map[string]any) []string {
			return []string{field(m, "name"), field(m, "repo"), field(m, "branch"), field(m, "path"), field(m, "interval"),
				field(m, "enabled"), when(m, "lastSyncAt"), short(field(m, "lastCommit")), field(m, "lastError")}
		},
	},
}

// field reads a (dotted) path from decoded JSON and renders it as one table
// cell; missing, null and empty values are "-".
func field(m map[string]any, path string) string {
	var cur any = m
	for part := range strings.SplitSeq(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "-"
		}
		cur = obj[part]
	}
	switch v := cur.(type) {
	case nil:
		return "-"
	case string:
		if v == "" {
			return "-"
		}
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	b, _ := json.Marshal(cur)
	return string(b)
}

// when renders a timestamp field in local time.
func when(m map[string]any, path string) string {
	s := field(m, path)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// byteSize renders a byte count with a binary unit; zero and missing are "-".
func byteSize(m map[string]any, path string) string {
	f, err := strconv.ParseFloat(field(m, path), 64)
	if err != nil || f <= 0 {
		return "-"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatFloat(f, 'f', 0, 64) + " B"
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + units[i]
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// managed renders managedBy as "source", with flags for drift and conflict.
func managed(m map[string]any) string {
	src := field(m, "managedBy.source")
	if src == "-" {
		return "-"
	}
	if field(m, "managedBy.conflict") == "true" {
		return src + " (conflict)"
	}
	if field(m, "managedBy.drifted") == "true" {
		return src + " (drifted)"
	}
	return src
}

// health renders the health of a resource summary (a string) or detail (an
// object with a status).
func health(m map[string]any) string {
	if _, ok := m["health"].(map[string]any); ok {
		return field(m, "health.status")
	}
	return field(m, "health")
}

// list renders a string array field comma-separated, or empty when it has
// no items.
func list(m map[string]any, key, empty string) string {
	items, _ := m[key].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return empty
	}
	return strings.Join(out, ",")
}

func triggers(m map[string]any) string {
	list, _ := m["triggers"].([]any)
	var types []string
	for _, t := range list {
		if tm, ok := t.(map[string]any); ok {
			types = append(types, strings.TrimPrefix(field(tm, "type"), "trigger."))
		}
	}
	if len(types) == 0 {
		return "-"
	}
	return strings.Join(types, ",")
}

func (a *app) get(ctx context.Context, args []string) error {
	fs, conn := a.clientFlags("get")
	format := fs.String("o", "table", "output format: table, json or yaml")
	limit := fs.Int("limit", 0, "maximum number of items to list (default 100, 50 for runs)")
	typ := fs.String("type", "", "resource type to list: service, machine or router (resources only)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usagef("get: expected <resources|policies|runs|locks|sources|connections> [name] or backups <resource>")
	}
	res, ok := getKinds[pos[0]]
	if !ok {
		return usagef("get: unknown kind %q; expected resources, policies, runs, locks, sources, connections or backups", pos[0])
	}
	if res.parent != nil {
		if len(pos) != 2 {
			return usagef("get: expected backups <resource>")
		}
		res.list = res.parent(pos[1])
	}
	if *typ != "" && !res.typed {
		return usagef("get: --type only applies to resources")
	}
	switch *format {
	case "table", "json", "yaml":
	default:
		return usagef("get: -o must be table, json or yaml")
	}
	if *limit < 0 {
		return usagef("get: --limit must not be negative")
	}
	cl, err := a.newClient(conn)
	if err != nil {
		return err
	}

	var items []map[string]any
	single := len(pos) == 2 && res.parent == nil
	switch {
	case single && res.show != nil:
		var m map[string]any
		if err := cl.JSON(ctx, "GET", res.show(pos[1]), nil, nil, &m); err != nil {
			return apiError(err)
		}
		items = []map[string]any{m}
	default:
		max := *limit
		if max == 0 {
			max = 100
			if res.perPage > 0 {
				max = res.perPage
			}
		}
		all, truncated, err := listAll(ctx, cl, res, max, *typ)
		if err != nil {
			return apiError(err)
		}
		if single {
			for _, m := range all {
				if field(m, res.key) == pos[1] {
					items = append(items, m)
				}
			}
			if len(items) == 0 {
				return fmt.Errorf("%s %q not found", strings.TrimSuffix(pos[0], "s"), pos[1])
			}
		} else {
			items = all
			if truncated {
				_, _ = fmt.Fprintf(a.stderr, "showing the first %d; use --limit to see more\n", max)
			}
		}
	}
	return a.render(res, items, single && res.show != nil, *format)
}

// listAll follows the cursor until max items are collected. typ, when set,
// filters resources by type.
func listAll(ctx context.Context, cl *client.Client, res getKind, max int, typ string) (items []map[string]any, truncated bool, err error) {
	cursor := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(min(max-len(items), 500))}}
		if typ != "" {
			q.Set("type", typ)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"nextCursor"`
		}
		if err := cl.JSON(ctx, "GET", res.list, q, nil, &page); err != nil {
			return nil, false, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return items, false, nil
		}
		if len(items) >= max {
			return items, true, nil
		}
		cursor = *page.NextCursor
	}
}

func (a *app) render(res getKind, items []map[string]any, one bool, format string) error {
	switch format {
	case "json", "yaml":
		var v any = items
		if one {
			v = items[0]
		}
		if format == "json" {
			enc := json.NewEncoder(a.stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		}
		// Going through JSON keeps the API's field names and number types.
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var generic any
		if err := yaml.Unmarshal(raw, &generic); err != nil {
			return err
		}
		out, err := yaml.Marshal(generic)
		if err != nil {
			return err
		}
		_, err = a.stdout.Write(out)
		return err
	}
	t := newTable(a.stdout, res.header...)
	for _, m := range items {
		t.row(res.row(m)...)
	}
	return t.flush()
}
