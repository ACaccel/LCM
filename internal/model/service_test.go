package model_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const serviceHead = "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: app, labels: { tier: web } }\n"

const fullService = serviceHead + `spec:
  runtime:
    type: systemd
    unit: app.service
  source:
    repo: /srv/app
    branch: main
    build: npm ci && npm run build
    timeout: 20m
  backup: { command: "tar czf /tmp/b.tgz /srv/app", timeout: 30m }
  jobs:
    reindex: { command: "app reindex", timeout: 10m }
  dependsOn: [db]
  checks:
    - { name: http, type: http, props: { url: "http://127.0.0.1:8080/health" } }
`

func TestDecodeService(t *testing.T) {
	s, err := model.DecodeService([]byte(fullService))
	if err != nil {
		t.Fatal(err)
	}
	if s.DocKind() != model.KindService || s.DocName() != "app" {
		t.Fatalf("header: %v %v", s.DocKind(), s.DocName())
	}
	sp := s.Spec
	if sp.Host != "local" || sp.Runtime.Scope != "system" {
		t.Fatalf("defaults not applied: %+v", sp)
	}
	if sp.Source.Remote != "origin" || sp.Source.EffectiveTimeout() != 20*time.Minute {
		t.Fatalf("source = %+v", sp.Source)
	}
	if sp.Jobs["reindex"].EffectiveTimeout() != 10*time.Minute || sp.Backup.EffectiveTimeout() != 30*time.Minute {
		t.Fatalf("command timeouts wrong")
	}
	if sp.Checks[0].Props["url"] != "http://127.0.0.1:8080/health" {
		t.Fatalf("check props = %v", sp.Checks[0].Props)
	}

	b, err := model.Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := model.DecodeService(b)
	if err != nil {
		t.Fatalf("round trip: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Fatalf("round trip changed the document:\n%+v\n%+v", s, s2)
	}
	if doc, err := model.Decode([]byte(fullService)); err != nil || doc.DocKind() != model.KindService {
		t.Fatalf("Decode: %v %v", doc, err)
	}
}

func TestDecodeServiceRuntimes(t *testing.T) {
	ok := map[string]string{
		"systemd user":  "type: systemd\n    unit: app.service\n    scope: user",
		"timer":         "type: systemd\n    unit: backup@1.timer",
		"docker":        "type: docker\n    container: my_app-1",
		"compose":       "type: compose\n    project: shop\n    file: /srv/shop/compose.yaml",
		"command":       "type: command\n    start: ./run.sh\n    stop: ./stop.sh",
		"command all":   "type: command\n    start: a\n    stop: b\n    restart: c\n    status: d",
		"external":      "type: external",
		"command empty": "type: command\n    start: a\n    stop: b\n    status: ''",
		"status only":   "type: command\n    status: d",
	}
	for name, rt := range ok {
		t.Run("ok/"+name, func(t *testing.T) {
			if _, err := model.DecodeService([]byte(serviceHead + "spec:\n  runtime:\n    " + rt + "\n")); err != nil {
				t.Fatal(err)
			}
		})
	}

	bad := []struct{ name, rt, path, msg string }{
		{"missing type", "unit: a.service", "spec.runtime.type", "required"},
		{"unknown type", "type: kube", "spec.runtime.type", "one of"},
		{"systemd no unit", "type: systemd", "spec.runtime.unit", "required"},
		{"systemd bad unit", "type: systemd\n    unit: 'a b.service'", "spec.runtime.unit", "unit name"},
		{"systemd option-like unit", "type: systemd\n    unit: '-h.service'", "spec.runtime.unit", "unit name"},
		{"systemd no suffix", "type: systemd\n    unit: app", "spec.runtime.unit", "unit name"},
		{"systemd bad scope", "type: systemd\n    unit: a.service\n    scope: global", "spec.runtime.scope", "system or user"},
		{"systemd with container", "type: systemd\n    unit: a.service\n    container: c", "spec.runtime.container", "not valid for runtime type systemd"},
		{"docker no container", "type: docker", "spec.runtime.container", "required"},
		{"docker bad container", "type: docker\n    container: '/etc'", "spec.runtime.container", "invalid"},
		{"docker with unit", "type: docker\n    container: c\n    unit: a.service", "spec.runtime.unit", "not valid"},
		{"compose no project", "type: compose\n    file: /a.yml", "spec.runtime.project", "required"},
		{"compose relative file", "type: compose\n    project: p\n    file: a.yml", "spec.runtime.file", "absolute"},
		{"compose unclean file", "type: compose\n    project: p\n    file: /srv/../a.yml", "spec.runtime.file", "absolute"},
		{"compose upper project", "type: compose\n    project: P\n    file: /a.yml", "spec.runtime.project", "lowercase"},
		{"command with scope", "type: command\n    start: x\n    stop: y\n    scope: user", "spec.runtime.scope", "not valid"},
		{"external with unit", "type: external\n    unit: a.service", "spec.runtime.unit", "not valid"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeService([]byte(serviceHead + "spec:\n  runtime:\n    " + tt.rt + "\n"))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}

	t.Run("unknown runtime field", func(t *testing.T) {
		_, err := model.DecodeService([]byte(serviceHead + "spec:\n  runtime:\n    type: systemd\n    unit: a.service\n    image: x\n"))
		assertIssue(t, err, "", "field image not found")
	})
}

func TestValidateServiceFields(t *testing.T) {
	rt := "spec:\n  runtime: { type: external }\n"
	bad := []struct{ name, doc, path, msg string }{
		{"name", "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: Bad_Name }\n" + rt, "metadata.name", "lowercase"},
		{"host", serviceHead + rt + "  host: ssh1\n", "spec.host", "local"},
		{"source no repo", serviceHead + rt + "  source: { branch: main }\n", "spec.source.repo", "required"},
		{"source relative repo", serviceHead + rt + "  source: { repo: srv/app, branch: main }\n", "spec.source.repo", "absolute"},
		{"source no branch", serviceHead + rt + "  source: { repo: /srv/app }\n", "spec.source.branch", "required"},
		{"source option-like branch", serviceHead + rt + "  source: { repo: /srv/app, branch: '--upload-pack=x' }\n", "spec.source.branch", "invalid"},
		{"source dotdot branch", serviceHead + rt + "  source: { repo: /srv/app, branch: a..b }\n", "spec.source.branch", "invalid"},
		{"source remote", serviceHead + rt + "  source: { repo: /srv/app, branch: main, remote: '-x' }\n", "spec.source.remote", "invalid"},
		{"source timeout", serviceHead + rt + "  source: { repo: /srv/app, branch: main, timeout: -1m }\n", "spec.source.timeout", "negative"},
		{"source unknown field", serviceHead + rt + "  source: { repo: /srv/app, branch: main, tag: v1 }\n", "", "field tag not found"},
		{"backup empty", serviceHead + rt + "  backup: { timeout: 1m }\n", "spec.backup.command", "required"},
		{"job name", serviceHead + rt + "  jobs: { Bad: { command: x } }\n", "spec.jobs.Bad", "job names"},
		{"job empty", serviceHead + rt + "  jobs: { ok: { command: '' } }\n", "spec.jobs.ok.command", "required"},
		{"depends self", serviceHead + rt + "  dependsOn: [app]\n", "spec.dependsOn[0]", "itself"},
		{"depends dup", serviceHead + rt + "  dependsOn: [db, db]\n", "spec.dependsOn[1]", "duplicate"},
		{"depends name", serviceHead + rt + "  dependsOn: ['Bad Name']\n", "spec.dependsOn[0]", "invalid"},
		{"check dup", serviceHead + rt + "  checks: [{ name: a, type: tcp }, { name: a, type: tcp }]\n", "spec.checks[1].name", "duplicate"},
		{"check timeout", serviceHead + rt + "  checks: [{ name: a, type: tcp, interval: 5s, timeout: 10s }]\n", "spec.checks[0].timeout", "shorter"},
		{"check props keys", serviceHead + rt + "  checks: [{ name: a, type: tcp, props: { x: { true: 1 } } }]\n", "spec.checks[0].props", "string keys"},
		{"too many jobs", serviceHead + rt + "  jobs: {" + manyJobs() + "}\n", "spec.jobs", "at most"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeService([]byte(tt.doc))
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

func TestDecodeServiceKindMismatch(t *testing.T) {
	if _, err := model.DecodeService([]byte(validPolicy)); err == nil {
		t.Fatal("DecodeService accepted a policy")
	}
	if _, err := model.DecodePolicy([]byte(fullService)); err == nil {
		t.Fatal("DecodePolicy accepted a service")
	}
}
