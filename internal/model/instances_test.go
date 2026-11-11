package model_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const instancesDoc = managedService + `    limits: { memoryHigh: 384M, memoryMax: 512M, stopTimeout: 60s }
    instances:
      alpha: { start: dist/alpha.js }
      beta: { startArgs: [] }
      log-archive: { startArgs: [--archive], limits: { memoryMax: 1G } }
  checks:
    - { name: alpha-relay, instance: alpha, type: tcp, props: { address: "127.0.0.1:4100" } }
    - { name: service-wide, type: tcp, props: { address: "127.0.0.1:4200" } }
`

func TestInstancesResolve(t *testing.T) {
	r, err := model.DecodeResource([]byte(instancesDoc))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Service()
	got := cfg.ResolvedInstances()
	limits := func(high, ceiling string) *model.UnitLimits {
		return &model.UnitLimits{MemoryHigh: high, MemoryMax: ceiling, StopTimeout: model.Duration(60 * time.Second)}
	}
	want := []model.ResolvedInstance{
		{Name: "alpha", Start: "dist/alpha.js", StartArgs: []string{"--port-from-env", "mode=prod"}, Limits: limits("384M", "512M")},
		{Name: "beta", Start: "server/main.mjs", StartArgs: []string{}, Limits: limits("384M", "512M")},
		{Name: "log-archive", Start: "server/main.mjs", StartArgs: []string{"--archive"}, Limits: limits("384M", "1G")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %+v\nwant %+v", got, want)
	}
	if rt := cfg.Runtime("managed"); !reflect.DeepEqual(rt.Instances, []string{"alpha", "beta", "log-archive"}) || !rt.RunsProcess() {
		t.Fatalf("runtime = %+v", rt)
	}
	if r.Spec.Checks[0].Instance != "alpha" {
		t.Fatalf("check = %+v", r.Spec.Checks[0])
	}

	// An explicit empty startArgs survives the canonical form.
	enc, err := model.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enc), `"beta":{"startArgs":[]}`) {
		t.Fatalf("encoded = %s", enc)
	}
	if r2, err := model.DecodeResource(enc); err != nil || !reflect.DeepEqual(r, r2) {
		t.Fatalf("round trip: %v\n%s", err, enc)
	}

	single, err := model.DecodeResource([]byte(managedService))
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := single.Service()
	if sc.ResolvedInstances() != nil || sc.Runtime("managed").Instances != nil {
		t.Fatalf("single-instance service resolves instances: %+v", sc)
	}
}

func TestInstancesWithoutServiceStart(t *testing.T) {
	doc := strings.Replace(instancesDoc, "    start: server/main.mjs\n", "", 1)
	doc = strings.Replace(doc, "beta: { startArgs: [] }", "beta: { start: dist/beta.js }", 1)
	doc = strings.Replace(doc, "log-archive: { startArgs", "log-archive: { start: dist/archive.js, startArgs", 1)
	r, err := model.DecodeResource([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Service()
	if rt := cfg.Runtime("managed"); rt.Start != "" || !rt.RunsProcess() {
		t.Fatalf("runtime = %+v", rt)
	}
}

func TestInstancesInvalid(t *testing.T) {
	var many strings.Builder
	for i := range model.MaxInstances + 1 {
		many.WriteString("      i" + string(rune('a'+i)) + ": {}\n")
	}
	bad := []struct{ name, from, to, path, msg string }{
		{"name with double dash", "log-archive:", "msg--archive:", "spec.config.instances.msg--archive", "single '-'"},
		{"name with trailing dash", "log-archive:", "msg-:", "spec.config.instances.msg-", "single '-'"},
		{"name too long", "log-archive:", "abcdefghijklmnopqrstuvwxy:", "spec.config.instances.abcdefghijklmnopqrstuvwxy", "max 24"},
		{"upper case", "alpha:", "Konata:", "spec.config.instances.Konata", "lowercase"},
		{"reserved name", "beta:", "backup:", "spec.config.instances.backup", "not backup"},
		{"start path", "start: dist/alpha.js", "start: /bin/sh", "spec.config.instances.alpha.start", "release-relative"},
		{"start args", "startArgs: [--archive]", "startArgs: ['a b']", "spec.config.instances.log-archive.startArgs", "may only contain"},
		{"limits", "limits: { memoryMax: 1G }", "limits: { memoryMax: 20G }", "spec.config.instances.log-archive.limits.memoryMax", "between 1M and 16G"},
		{"merged limits", "limits: { memoryMax: 1G }", "limits: { memoryHigh: 600M }", "spec.config.instances.log-archive.limits", "once merged"},
		{"unknown key", "beta: { startArgs: [] }", "beta: { port: 4000 }", "", "field port not found"},
		{"duplicate key", "beta: { startArgs: [] }", "alpha: {}", "", "already defined"},
		{"empty", "    instances:\n      alpha: { start: dist/alpha.js }\n      beta: { startArgs: [] }\n      log-archive: { startArgs: [--archive], limits: { memoryMax: 1G } }\n",
			"    instances: {}\n", "spec.config.instances", "at least one"},
		{"too many", "      alpha: { start: dist/alpha.js }\n      beta: { startArgs: [] }\n      log-archive: { startArgs: [--archive], limits: { memoryMax: 1G } }\n",
			many.String(), "spec.config.instances", "at most 16"},
		{"check names unknown instance", "instance: alpha, type: tcp", "instance: gamma, type: tcp", "spec.checks[0].instance", "must name an instance"},
		{"no start anywhere", "    start: server/main.mjs\n", "", "spec.config.instances.beta.start", "is required"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(instancesDoc, tt.from) {
				t.Fatalf("fixture lacks %q", tt.from)
			}
			doc := strings.Replace(instancesDoc, tt.from, tt.to, 1)
			if tt.name == "too many" {
				doc = strings.Replace(doc, "    - { name: alpha-relay, instance: alpha, type: tcp, props: { address: \"127.0.0.1:4100\" } }\n", "", 1)
			}
			_, err := model.DecodeResource([]byte(doc))
			if tt.path == "" {
				if err == nil || !strings.Contains(err.Error(), tt.msg) {
					t.Fatalf("err = %v, want %q", err, tt.msg)
				}
				return
			}
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}

func TestInstancesOnlyForManagedWithProcess(t *testing.T) {
	// A static-only service runs no process, so it cannot have instances
	// without a start each.
	static := strings.Replace(managedService, "    start: server/main.mjs\n    startArgs: [--port-from-env, mode=prod]\n    migrate: [node, scripts/migrate.mjs]\n",
		"    publish: { dir: www }\n    instances:\n      a: {}\n", 1)
	_, err := model.DecodeResource([]byte(static))
	assertIssue(t, err, "spec.config.instances.a.start", "is required")

	systemd := resourceHead + `spec:
  type: service
  adapter: systemd
  config:
    unit: app.service
    instances: { a: {} }
`
	_, err = model.DecodeResource([]byte(systemd))
	assertIssue(t, err, "spec.config.instances", "not valid for adapter systemd")

	check := resourceHead + `spec:
  type: service
  adapter: systemd
  config: { unit: app.service }
  checks:
    - { name: a, type: tcp, instance: a, props: { address: "127.0.0.1:1" } }
`
	_, err = model.DecodeResource([]byte(check))
	assertIssue(t, err, "spec.checks[0].instance", "only valid on a service with adapter managed")
}

func TestMongoAllDatabases(t *testing.T) {
	const all = managedService + `    backup:
      sources:
        - { type: mongodb, all: true, users: true }
        - { type: files, path: data/ }
`
	r, err := model.DecodeResource([]byte(all))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := r.Service()
	if s := cfg.Backup.Sources[0]; !s.All || !s.Users || s.Database != "" {
		t.Fatalf("source = %+v", s)
	}
	bad := []struct{ name, from, to, path, msg string }{
		{"all and database", "all: true", "all: true, database: app", "spec.config.backup.sources[0].database", "cannot be combined with all"},
		{"neither", "all: true, ", "", "spec.config.backup.sources[0].database", "or set all: true"},
		{"second mongodb", "        - { type: files, path: data/ }\n", "        - { type: mongodb, database: other }\n", "spec.config.backup.sources", "only mongodb source"},
		{"all on postgres", "type: mongodb, all: true, users: true", "type: postgres, all: true", "spec.config.backup.sources[0].all", "not valid for source type postgres"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeResource([]byte(strings.Replace(all, tt.from, tt.to, 1)))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}
