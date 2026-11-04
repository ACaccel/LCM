package model_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const resourceHead = "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: app, labels: { tier: web } }\n"

const fullService = resourceHead + `spec:
  type: service
  adapter: systemd
  config:
    unit: app.service
    source:
      repo: /srv/app
      branch: main
      build: npm ci && npm run build
      timeout: 20m
    backup: { command: "tar czf /tmp/b.tgz /srv/app", timeout: 30m }
    jobs:
      reindex: { command: "app reindex", timeout: 10m }
  relations:
    - { type: dependsOn, target: db }
  checks:
    - { name: http, type: http, props: { url: "http://127.0.0.1:8080/health" } }
`

func TestDecodeServiceResource(t *testing.T) {
	r, err := model.DecodeResource([]byte(fullService))
	if err != nil {
		t.Fatal(err)
	}
	if r.DocKind() != model.KindResource || r.DocName() != "app" {
		t.Fatalf("header: %v %v", r.DocKind(), r.DocName())
	}
	cfg, ok := r.Service()
	if !ok {
		t.Fatalf("config = %T", r.Spec.Config)
	}
	if _, ok := r.Machine(); ok {
		t.Fatal("service resource reported a machine config")
	}
	if cfg.Scope != "system" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	rt := cfg.Runtime(r.Spec.Adapter)
	if rt.Type != model.RuntimeSystemd || rt.Unit != "app.service" || rt.EffectiveScope() != model.ScopeSystem {
		t.Fatalf("runtime = %+v", rt)
	}
	if cfg.Source.Remote != "origin" || cfg.Source.EffectiveTimeout() != 20*time.Minute {
		t.Fatalf("source = %+v", cfg.Source)
	}
	if cfg.Jobs["reindex"].EffectiveTimeout() != 10*time.Minute || cfg.Backup.EffectiveTimeout() != 30*time.Minute {
		t.Fatalf("command timeouts wrong")
	}
	if r.Spec.Checks[0].Props["url"] != "http://127.0.0.1:8080/health" {
		t.Fatalf("check props = %v", r.Spec.Checks[0].Props)
	}
	if r.Spec.Relations[0] != (model.Relation{Type: model.RelationDependsOn, Target: "db"}) {
		t.Fatalf("relations = %v", r.Spec.Relations)
	}

	b, err := model.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := model.DecodeResource(b)
	if err != nil {
		t.Fatalf("round trip: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(r, r2) {
		t.Fatalf("round trip changed the document:\n%+v\n%+v", r, r2)
	}
	b2, err := model.Encode(r2)
	if err != nil || string(b) != string(b2) {
		t.Fatalf("canonical form not stable: %v\n%s\n%s", err, b, b2)
	}

	// json.Unmarshal goes through the same strict typed decoding.
	var r3 model.Resource
	if err := json.Unmarshal(b, &r3); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, &r3) {
		t.Fatalf("json.Unmarshal differs:\n%+v\n%+v", r, &r3)
	}
	bad := strings.Replace(string(b), `"unit":`, `"image":"x","unit":`, 1)
	if err := json.Unmarshal([]byte(bad), &r3); err == nil || !strings.Contains(err.Error(), "field image not found") {
		t.Fatalf("unknown config field via json: %v", err)
	}
}

func TestDecodeServiceAdapters(t *testing.T) {
	head := resourceHead + "spec:\n  type: service\n"
	ok := map[string]string{
		"systemd user":  "adapter: systemd\n  config: { unit: app.service, scope: user }",
		"timer":         "adapter: systemd\n  config: { unit: backup@1.timer }",
		"docker":        "adapter: docker\n  config: { container: my_app-1 }",
		"compose":       "adapter: compose\n  config: { project: shop, file: /srv/shop/compose.yaml }",
		"command":       "adapter: command\n  config: { start: ./run.sh, stop: ./stop.sh }",
		"command all":   "adapter: command\n  config: { start: a, stop: b, restart: c, status: d }",
		"external":      "adapter: external",
		"external {}":   "adapter: external\n  config: {}",
		"command empty": "adapter: command\n  config: { start: a, stop: b, status: '' }",
	}
	for name, body := range ok {
		t.Run("ok/"+name, func(t *testing.T) {
			if _, err := model.DecodeResource([]byte(head + "  " + body + "\n")); err != nil {
				t.Fatal(err)
			}
		})
	}

	bad := []struct{ name, body, path, msg string }{
		{"missing adapter", "config: { unit: a.service }", "spec.adapter", "required"},
		{"unknown adapter", "adapter: kube", "spec.adapter", "one of systemd, docker"},
		{"machine adapter", "adapter: local", "spec.adapter", "for type service"},
		{"systemd no unit", "adapter: systemd", "spec.config.unit", "required"},
		{"systemd bad unit", "adapter: systemd\n  config: { unit: 'a b.service' }", "spec.config.unit", "unit name"},
		{"systemd option-like unit", "adapter: systemd\n  config: { unit: '-h.service' }", "spec.config.unit", "unit name"},
		{"systemd no suffix", "adapter: systemd\n  config: { unit: app }", "spec.config.unit", "unit name"},
		{"systemd bad scope", "adapter: systemd\n  config: { unit: a.service, scope: global }", "spec.config.scope", "system or user"},
		{"systemd with container", "adapter: systemd\n  config: { unit: a.service, container: c }", "spec.config.container", "not valid for adapter systemd"},
		{"docker no container", "adapter: docker", "spec.config.container", "required"},
		{"docker bad container", "adapter: docker\n  config: { container: '/etc' }", "spec.config.container", "invalid"},
		{"docker with unit", "adapter: docker\n  config: { container: c, unit: a.service }", "spec.config.unit", "not valid"},
		{"compose no project", "adapter: compose\n  config: { file: /a.yml }", "spec.config.project", "required"},
		{"compose relative file", "adapter: compose\n  config: { project: p, file: a.yml }", "spec.config.file", "absolute"},
		{"compose unclean file", "adapter: compose\n  config: { project: p, file: /srv/../a.yml }", "spec.config.file", "absolute"},
		{"compose upper project", "adapter: compose\n  config: { project: P, file: /a.yml }", "spec.config.project", "lowercase"},
		{"command with scope", "adapter: command\n  config: { start: x, scope: user }", "spec.config.scope", "not valid"},
		{"external with unit", "adapter: external\n  config: { unit: a.service }", "spec.config.unit", "not valid"},
		{"connection", "adapter: external\n  connection: home", "spec.connection", "not valid for type service"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeResource([]byte(head + "  " + tt.body + "\n"))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}

	t.Run("unknown config field", func(t *testing.T) {
		_, err := model.DecodeResource([]byte(head + "  adapter: systemd\n  config:\n    unit: a.service\n    image: x\n"))
		// The strict decoder reports the line in the whole document.
		assertIssue(t, err, "", "line 9: field image not found in type model.ServiceConfig")
	})
	t.Run("v1 runtime block", func(t *testing.T) {
		_, err := model.DecodeResource([]byte(head + "  adapter: systemd\n  config:\n    runtime: { type: systemd }\n"))
		assertIssue(t, err, "", "field runtime not found")
	})
}

func TestValidateServiceConfigFields(t *testing.T) {
	head := resourceHead + "spec:\n  type: service\n  adapter: external\n"
	cfg := func(s string) string { return head + "  config: " + s + "\n" }
	bad := []struct{ name, doc, path, msg string }{
		{"name", "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: Bad_Name }\nspec: { type: service, adapter: external }\n", "metadata.name", "lowercase"},
		{"source no repo", cfg("{ source: { branch: main } }"), "spec.config.source.repo", "required"},
		{"source relative repo", cfg("{ source: { repo: srv/app, branch: main } }"), "spec.config.source.repo", "absolute"},
		{"source no branch", cfg("{ source: { repo: /srv/app } }"), "spec.config.source.branch", "required"},
		{"source option-like branch", cfg("{ source: { repo: /srv/app, branch: '--upload-pack=x' } }"), "spec.config.source.branch", "invalid"},
		{"source dotdot branch", cfg("{ source: { repo: /srv/app, branch: a..b } }"), "spec.config.source.branch", "invalid"},
		{"source remote", cfg("{ source: { repo: /srv/app, branch: main, remote: '-x' } }"), "spec.config.source.remote", "invalid"},
		{"source timeout", cfg("{ source: { repo: /srv/app, branch: main, timeout: -1m } }"), "spec.config.source.timeout", "negative"},
		{"source unknown field", cfg("{ source: { repo: /srv/app, branch: main, tag: v1 } }"), "", "field tag not found"},
		{"backup empty", cfg("{ backup: { timeout: 1m } }"), "spec.config.backup.command", "required"},
		{"job name", cfg("{ jobs: { Bad: { command: x } } }"), "spec.config.jobs.Bad", "job names"},
		{"job empty", cfg("{ jobs: { ok: { command: '' } } }"), "spec.config.jobs.ok.command", "required"},
		{"too many jobs", cfg("{ jobs: {" + manyJobs() + "} }"), "spec.config.jobs", "at most"},
		{"host removed", head + "  host: local\n", "", "field host not found"},
		{"dependsOn removed", head + "  dependsOn: [db]\n", "", "field dependsOn not found"},
		{"check dup", head + "  checks: [{ name: a, type: tcp }, { name: a, type: tcp }]\n", "spec.checks[1].name", "duplicate"},
		{"check timeout", head + "  checks: [{ name: a, type: tcp, interval: 5s, timeout: 10s }]\n", "spec.checks[0].timeout", "shorter"},
		{"check props keys", head + "  checks: [{ name: a, type: tcp, props: { x: { true: 1 } } }]\n", "spec.checks[0].props", "string keys"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeResource([]byte(tt.doc))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}

func manyJobs() string {
	var b strings.Builder
	for i := range model.MaxJobs + 1 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "j%d: { command: x }", i)
	}
	return b.String()
}

func TestRelations(t *testing.T) {
	head := resourceHead + "spec:\n  type: service\n  adapter: external\n  relations:\n"
	ok := head + "    - { type: dependsOn, target: db }\n    - { type: runsOn, target: host }\n    - { type: controlPath, target: router }\n    - { type: runsOn, target: db }\n"
	if _, err := model.DecodeResource([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	bad := []struct{ name, rels, path, msg string }{
		{"type", "    - { type: uses, target: db }\n", "spec.relations[0].type", "must be one of"},
		{"no type", "    - { target: db }\n", "spec.relations[0].type", "required"},
		{"self", "    - { type: dependsOn, target: app }\n", "spec.relations[0].target", "itself"},
		{"dup", "    - { type: dependsOn, target: db }\n    - { type: dependsOn, target: db }\n", "spec.relations[1]", "duplicate"},
		{"name", "    - { type: dependsOn, target: 'Bad Name' }\n", "spec.relations[0].target", "invalid"},
		{"field", "    - { type: dependsOn, target: db, weight: 1 }\n", "", "field weight not found"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeResource([]byte(head + tt.rels))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}

func TestMachineResource(t *testing.T) {
	base := "apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: workstation}\nspec:\n  type: machine\n  adapter: local\n"
	r, err := model.DecodeResource([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := r.Machine()
	if !ok || cfg.Interval.D() != 30*time.Second || cfg.StaleAfter.D() != 90*time.Second || cfg.Filesystems[0] != "/" ||
		cfg.Thresholds != (model.MachineThresholds{CPUPercent: 90, MemoryPercent: 90, DiskPercent: 90, Consecutive: 3, Hysteresis: 5}) {
		t.Fatalf("defaults: %+v", cfg)
	}
	b, err := model.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = model.DecodeResource(b); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"config: {interval: 1ms}":                       "spec.config.interval",
		"config: {journal: true}":                       "spec.config.units",
		"config: {units: [--danger.service]}":           "spec.config.units[0]",
		"config: {filesystems: [/tmp/../etc]}":          "spec.config.filesystems[0]",
		"config: {thresholds: {cpuPercent: .nan}}":      "spec.config.thresholds.cpuPercent",
		"config: {thresholds: {hysteresis: 95}}":        "spec.config.thresholds.hysteresis",
		"config: {interval: 10s, staleAfter: 15s}":      "spec.config.staleAfter",
		"connection: x":                                 "spec.connection",
		"config: {host: ssh}":                           "",
		"config: {thresholds: {consecutive: 500}}":      "spec.config.thresholds.consecutive",
		"config: {interfaces: [this-name-is-too-long]}": "spec.config.interfaces[0]",
	}
	for body, path := range bad {
		_, err := model.DecodeResource([]byte(base + "  " + body + "\n"))
		if err == nil {
			t.Errorf("accepted %s", body)
			continue
		}
		if path != "" {
			assertIssue(t, err, path, "")
		}
	}
	if _, err := model.DecodeResource([]byte(strings.Replace(base, "adapter: local", "adapter: ssh", 1))); err == nil {
		t.Fatal("remote machine accepted")
	}
}

func TestRouterResource(t *testing.T) {
	base := "apiVersion: lcm/v2\nkind: Resource\nmetadata: {name: router}\nspec:\n  type: router\n  adapter: xiaomi\n"
	r, err := model.DecodeResource([]byte(base + "  connection: home-router\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := r.Router()
	if !ok || cfg.Interval.D() != time.Minute || cfg.StaleAfter.D() != 3*time.Minute ||
		cfg.Thresholds != (model.RouterThresholds{CPUPercent: 90, MemoryPercent: 90, Consecutive: 3, Hysteresis: 5}) {
		t.Fatalf("defaults: %+v", cfg)
	}
	if r.Spec.Connection != "home-router" {
		t.Fatalf("connection = %q", r.Spec.Connection)
	}
	bad := []struct{ body, path, msg string }{
		{"", "spec.connection", "required"},
		{"  connection: Home_Router\n", "spec.connection", "connection name"},
		{"  connection: r\n  config: {interval: 10s}\n", "spec.config.interval", "between 15s and 1h"},
		{"  connection: r\n  config: {interval: 2h}\n", "spec.config.interval", "between"},
		{"  connection: r\n  config: {thresholds: {memoryPercent: 120}}\n", "spec.config.thresholds.memoryPercent", "at most 100"},
		{"  connection: r\n  config: {thresholds: {diskPercent: 50}}\n", "", "field diskPercent not found"},
		{"  connection: r\n  config: {url: http://x}\n", "", "field url not found"},
	}
	for _, tt := range bad {
		t.Run(tt.body, func(t *testing.T) {
			_, err := model.DecodeResource([]byte(base + tt.body))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}

func TestResourceTypeErrors(t *testing.T) {
	head := "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: x }\n"
	_, err := model.DecodeResource([]byte(head + "spec: { type: printer, adapter: cups, config: { a: 1 }, checks: [{ name: Bad, type: tcp }] }\n"))
	assertIssue(t, err, "spec.type", "must be one of service, machine, router")
	// The rest of the document is still checked.
	assertIssue(t, err, "spec.checks[0].name", "lowercase")
	_, err = model.DecodeResource([]byte(head + "spec: {}\n"))
	assertIssue(t, err, "spec.type", "required")

	_, err = model.Decode([]byte("apiVersion: lcm/v1\nkind: Service\nmetadata: { name: x }\n"))
	assertIssue(t, err, "apiVersion", "lcm convert")
	_, err = model.Decode([]byte("apiVersion: lcm/v2\nkind: Service\nmetadata: { name: x }\n"))
	assertIssue(t, err, "kind", "Resource")
	if _, err := model.DecodeResource([]byte(validPolicy)); err == nil {
		t.Fatal("DecodeResource accepted a policy")
	}
	if _, err := model.DecodePolicy([]byte(fullService)); err == nil {
		t.Fatal("DecodePolicy accepted a resource")
	}
	if err := model.ValidateResource(&model.Resource{
		Header: model.Header{APIVersion: model.APIVersion, Kind: model.KindResource, Metadata: model.Metadata{Name: "x"}},
		Spec:   model.ResourceSpec{Type: model.ResourceService, Adapter: "external"},
	}); err == nil || !strings.Contains(err.Error(), "spec.config: is required") {
		t.Fatalf("missing config: %v", err)
	}
}

func TestResourceTypes(t *testing.T) {
	types := model.ResourceTypes()
	if len(types) != 3 || types[0].Type != "service" || types[2].Type != "router" || !types[2].NeedsConnection || types[0].NeedsConnection {
		t.Fatalf("types = %+v", types)
	}
	if len(types[0].Adapters) != 5 || types[1].Adapters[0] != "local" || types[2].Adapters[0] != "xiaomi" {
		t.Fatalf("adapters = %+v", types)
	}
	types[0].Adapters[0] = "changed"
	if model.ResourceTypes()[0].Adapters[0] != "systemd" {
		t.Fatal("descriptors share the registry's slices")
	}
}

func TestEncodeYAML(t *testing.T) {
	r, err := model.DecodeResource([]byte(fullService))
	if err != nil {
		t.Fatal(err)
	}
	y, err := model.EncodeYAML(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(y), "apiVersion: lcm/v2\nkind: Resource\n") || !strings.Contains(string(y), "  config:\n    unit: app.service\n") {
		t.Fatalf("yaml:\n%s", y)
	}
	r2, err := model.DecodeResource(y)
	if err != nil || !reflect.DeepEqual(r, r2) {
		t.Fatalf("yaml round trip: %v", err)
	}
}
