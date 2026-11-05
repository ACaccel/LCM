package model_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

const v1Service = `apiVersion: lcm/v1
kind: Service
metadata: { name: web, labels: { tier: web } }
spec:
  host: local
  runtime: { type: systemd, unit: web.service, scope: user }
  backup: { command: "tar czf /tmp/web.tgz /srv/web" }
  jobs: { reindex: { command: "web reindex" } }
  dependsOn: [db, cache]
  checks:
    - { name: http, type: http, props: { url: "http://127.0.0.1:8080/" } }
`

const v2Service = `apiVersion: lcm/v2
kind: Resource
metadata: { name: web, labels: { tier: web } }
spec:
  type: service
  adapter: systemd
  config:
    unit: web.service
    scope: user
    backup: { command: "tar czf /tmp/web.tgz /srv/web" }
    jobs: { reindex: { command: "web reindex" } }
  checks:
    - { name: http, type: http, props: { url: "http://127.0.0.1:8080/" } }
  relations:
    - { type: dependsOn, target: db }
    - { type: dependsOn, target: cache }
`

const v1Policy = `apiVersion: lcm/v1
kind: Policy
metadata: { name: restart }
spec:
  params: { service: web }
  nodes:
    - id: start
      type: trigger.signal
      props: { kinds: [health.changed], services: ["{{ params.service }}"] }
      next: { out: [check] }
    - id: check
      type: flow.if
      props: { condition: "service[trigger.service].health == 'down' && trigger.service != ''" }
      next: { "true": [pick], "false": [] }
    - id: pick
      type: flow.switch
      props:
        cases:
          - { port: a, when: "service.web.state == 'running'" }
          - { port: b, when: "len(service) > 2" }
      next: { a: [restart], b: [notify] }
    - id: restart
      type: lifecycle.restart
      props: { service: "{{ trigger.service }}" }
    - id: notify
      type: notify.log
      props:
        message: "The service. {{ service[params.service].health }} {{ 'service.x' }} {{ nodes.restart.service }} {{ service }}"
        title: "service[x] is plain text"
`

func TestConvertService(t *testing.T) {
	docs, warns, err := model.ConvertV1([]byte(v1Service))
	if err != nil || len(warns) != 0 || len(docs) != 1 {
		t.Fatalf("convert: %v %v %d", err, warns, len(docs))
	}
	want, err := model.Decode([]byte(v2Service))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := model.Encode(docs[0])
	exp, _ := model.Encode(want)
	if string(got) != string(exp) {
		t.Fatalf("converted:\n%s\nwant:\n%s", got, exp)
	}

	// The JSON helper used by the storage migration reads the stored
	// canonical v1 JSON and writes the v2 canonical JSON.
	v1JSON := `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"web","labels":{"tier":"web"}},"spec":{"host":"local","runtime":{"type":"systemd","unit":"web.service","scope":"user"},"backup":{"command":"tar czf /tmp/web.tgz /srv/web"},"jobs":{"reindex":{"command":"web reindex"}},"dependsOn":["db","cache"],"checks":[{"name":"http","type":"http","props":{"url":"http://127.0.0.1:8080/"}}]}}`
	b, warns, err := model.ConvertServiceV1JSON([]byte(v1JSON))
	if err != nil || len(warns) != 0 || string(b) != string(exp) {
		t.Fatalf("json: %v %v\n%s\nwant\n%s", err, warns, b, exp)
	}
	if _, _, err := model.ConvertMachineV1JSON([]byte(v1JSON)); err == nil {
		t.Fatal("machine helper accepted a service")
	}
}

// A v1 source names a checkout and a remote; the URL it needs is in the
// checkout's git config, which an offline conversion cannot read.
func TestConvertServiceSourceRefused(t *testing.T) {
	doc := strings.Replace(v1Service, "  backup:", "  source: { repo: /srv/web, remote: upstream, branch: main }\n  backup:", 1)
	_, _, err := model.ConvertV1([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "spec.config.source.repo: source.repo and source.remote were replaced by source.url") ||
		strings.Contains(err.Error(), "url: is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestConvertServiceErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"remote host":   strings.Replace(v1Service, "host: local", "host: ssh1", 1),
		"unknown field": strings.Replace(v1Service, "host: local", "color: red", 1),
		"invalid":       strings.Replace(v1Service, "unit: web.service", "unit: web", 1),
		"version":       strings.Replace(v1Service, "lcm/v1", "lcm/v0", 1),
		"kind":          strings.Replace(v1Service, "kind: Service", "kind: Host", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := model.ConvertV1([]byte(doc)); err == nil || !strings.Contains(err.Error(), "document 1:") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestConvertMachine(t *testing.T) {
	v1 := "apiVersion: lcm/v1\nkind: Machine\nmetadata: {name: workstation}\nspec:\n  host: local\n  monitoring: {interval: 10s, units: [lcm.service], journal: true}\n"
	docs, _, err := model.ConvertV1([]byte(v1))
	if err != nil {
		t.Fatal(err)
	}
	r := docs[0].(*model.Resource)
	cfg, ok := r.Machine()
	if !ok || r.Spec.Adapter != "local" || cfg.Interval.D().Seconds() != 10 || cfg.StaleAfter.D().Seconds() != 30 || !cfg.Journal {
		t.Fatalf("machine = %+v %+v", r.Spec, cfg)
	}
	stored := `{"apiVersion":"lcm/v1","kind":"Machine","metadata":{"name":"workstation"},"spec":{"host":"local","monitoring":{"interval":"30s","staleAfter":"1m30s","filesystems":["/"],"journal":false,"thresholds":{"cpuPercent":90,"memoryPercent":90,"diskPercent":90,"consecutive":3,"hysteresis":5}}}}`
	b, _, err := model.ConvertMachineV1JSON([]byte(stored))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"workstation"},"spec":{"type":"machine","adapter":"local","config":{"interval":"30s","staleAfter":"1m30s","filesystems":["/"],"journal":false,"thresholds":{"cpuPercent":90,"memoryPercent":90,"diskPercent":90,"consecutive":3,"hysteresis":5}}}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	if _, _, err := model.ConvertV1([]byte(strings.Replace(v1, "host: local", "host: ssh", 1))); err == nil {
		t.Fatal("remote machine converted")
	}
}

func TestConvertPolicy(t *testing.T) {
	docs, warns, err := model.ConvertV1([]byte(v1Policy))
	if err != nil {
		t.Fatal(err)
	}
	p := docs[0].(*model.Policy)
	if p.APIVersion != model.APIVersion {
		t.Fatalf("apiVersion = %s", p.APIVersion)
	}
	n := map[string]model.Node{}
	for _, x := range p.Spec.Nodes {
		n[x.ID] = x
	}
	if got := n["start"].Props["resources"]; len(got.([]any)) != 1 || got.([]any)[0] != "{{ params.service }}" || n["start"].Props["services"] != nil {
		t.Fatalf("trigger props = %v", n["start"].Props)
	}
	if got := n["check"].Props["condition"]; got != "resource[trigger.resource].health == 'down' && trigger.resource != ''" {
		t.Fatalf("condition = %v", got)
	}
	cases := n["pick"].Props["cases"].([]any)
	if cases[0].(map[string]any)["when"] != "resource.web.state == 'running'" || cases[1].(map[string]any)["when"] != "len(service) > 2" {
		t.Fatalf("cases = %v", cases)
	}
	if got := n["restart"].Props; got["resource"] != "{{ trigger.resource }}" || got["service"] != nil {
		t.Fatalf("lifecycle props = %v", got)
	}
	msg := "The service. {{ resource[params.service].health }} {{ 'service.x' }} {{ nodes.restart.resource }} {{ service }}"
	if got := n["notify"].Props["message"]; got != msg {
		t.Fatalf("message = %v", got)
	}
	if got := n["notify"].Props["title"]; got != "service[x] is plain text" {
		t.Fatalf("title = %v", got)
	}
	if p.Spec.Params["service"] != "web" {
		t.Fatalf("params = %v", p.Spec.Params)
	}
	if got := n["restart"].Props["acknowledgeImpact"]; got != true {
		t.Fatalf("acknowledgeImpact = %v", got)
	}
	if len(warns) != 3 || !strings.Contains(warns[0], "spec.nodes[2].props.cases[1].when") || !strings.Contains(warns[1], "spec.nodes[4].props.message") || !strings.Contains(warns[0], "Policy restart") ||
		!strings.Contains(warns[2], "Policy restart: added acknowledgeImpact: true to lifecycle nodes restart") {
		t.Fatalf("warnings = %q", warns)
	}

	b, err := model.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := model.Encode(mustV1(t, v1Policy))
	jb, jwarns, err := model.ConvertPolicyV1JSON(stored)
	if err != nil || string(jb) != string(b) || len(jwarns) != 3 {
		t.Fatalf("json: %v %q\n%s\n%s", err, jwarns, jb, b)
	}
}

// mustV1 decodes a v1 policy as the old binary stored it: the same struct
// with the old apiVersion.
func mustV1(t *testing.T, doc string) *model.Policy {
	t.Helper()
	p, err := model.DecodePolicy([]byte(strings.Replace(doc, "lcm/v1", "lcm/v2", 1)))
	if err != nil {
		t.Fatal(err)
	}
	p.APIVersion = model.APIVersionV1
	return p
}

func TestConvertModuleAndMultiDoc(t *testing.T) {
	data := `apiVersion: lcm/v1
kind: Module
metadata: { name: m }
spec:
  params: [{ name: target }]
  nodes:
    - { id: in, type: module.input, next: { out: [act] } }
    - { id: act, type: lifecycle.stop, props: { service: "{{ params.target }}" }, next: { out: [out] } }
    - { id: out, type: module.output, props: { port: "{{ service?.db.health }}" } }
---
` + v2Service + "---\n" + v1Service
	docs, warns, err := model.ConvertV1([]byte(data))
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "Module m: added acknowledgeImpact") || len(docs) != 3 {
		t.Fatalf("convert: %v %q %d", err, warns, len(docs))
	}
	m := docs[0].(*model.Module)
	if m.Spec.Nodes[1].Props["resource"] != "{{ params.target }}" || m.Spec.Nodes[2].Props["port"] != "{{ resource?.db.health }}" {
		t.Fatalf("module nodes = %+v", m.Spec.Nodes)
	}
	a, _ := model.Encode(docs[1])
	b, _ := model.Encode(docs[2])
	if string(a) != string(b) {
		t.Fatalf("v2 passthrough and conversion differ:\n%s\n%s", a, b)
	}

	docs, _, err = model.ConvertV1([]byte(v1Service + "---\napiVersion: lcm/v1\nkind: Machine\nmetadata: {name: Bad}\n"))
	if err == nil || !strings.Contains(err.Error(), "document 2:") || len(docs) != 1 {
		t.Fatalf("err = %v, %d documents", err, len(docs))
	}
	if _, _, err := model.ConvertV1([]byte("  \n")); err == nil {
		t.Fatal("empty input accepted")
	}
}

func TestConvertPropClash(t *testing.T) {
	doc := strings.Replace(v1Policy, `props: { service: "{{ trigger.service }}" }`, `props: { service: a, resource: b }`, 1)
	_, warns, err := model.ConvertV1([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(warns, "\n"), `both "service" and "resource" are set`) {
		t.Fatalf("warnings = %q", warns)
	}
}

func TestConvertLifecycleOutputReferences(t *testing.T) {
	// warn marks a service member the rewrite leaves alone although it may
	// name a renamed field.
	cases := []struct {
		name, in, want string
		warn           bool
	}{
		{"template dotted", "{{ nodes.restart.service }}", "{{ nodes.restart.resource }}", false},
		{"template optional chaining", "{{ nodes.restart?.service }}", "{{ nodes.restart?.resource }}", false},
		{"index forms", `{{ nodes["restart"]['service'] }}`, `{{ nodes["restart"]['resource'] }}`, false},
		{"optional index", `{{ nodes.restart?.["service"] }}`, `{{ nodes.restart?.["resource"] }}`, false},
		{"in a longer expression", "id {{ nodes.restart.service + '/' + nodes.restart.action }}", "id {{ nodes.restart.resource + '/' + nodes.restart.action }}", false},
		{"plain text untouched", "nodes.restart.service", "nodes.restart.service", false},
		{"non-lifecycle node", "{{ nodes.notify.service }}", "{{ nodes.notify.service }}", true},
		{"unknown node", "{{ nodes.ghost.service }}", "{{ nodes.ghost.service }}", true},
		{"longer key", "{{ nodes.restart.serviceX }}", "{{ nodes.restart.serviceX }}", false},
		{"other key", "{{ nodes.restart.to }}", "{{ nodes.restart.to }}", false},
		{"nested key", "{{ nodes.restart.output.service }}", "{{ nodes.restart.output.service }}", true},
		{"member nodes", "{{ trigger.nodes.restart.service }}", "{{ trigger.nodes.restart.service }}", true},
		{"params untouched", "{{ params.service }}", "{{ params.service }}", false},
		{"params index untouched", `{{ params["service"] }}`, `{{ params["service"] }}`, false},
		{"trigger dotted", "{{ trigger.service }}", "{{ trigger.resource }}", false},
		{"trigger optional", "{{ trigger?.service }}", "{{ trigger?.resource }}", false},
		{"trigger index", `{{ trigger["service"] + trigger['service'] }}`, `{{ trigger["resource"] + trigger['resource'] }}`, false},
		{"trigger optional index", `{{ trigger?.["service"] }}`, `{{ trigger?.["resource"] }}`, false},
		{"trigger longer key", "{{ trigger.serviceType }}", "{{ trigger.serviceType }}", false},
		{"trigger nested", "{{ trigger.data.service }}", "{{ trigger.data.service }}", true},
		{"other index", `{{ event["service"] }}`, `{{ event["service"] }}`, true},
		{"string literal", `{{ "service" }}`, `{{ "service" }}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := `apiVersion: lcm/v1
kind: Policy
metadata: { name: p }
spec:
  nodes:
    - id: restart
      type: lifecycle.restart
      props: { service: web, acknowledgeImpact: true }
      next: { out: [notify, decide] }
    - id: notify
      type: notify.log
      props: { message: ` + strconv.Quote(c.in) + ` }
    - id: decide
      type: flow.if
      props: { condition: "nodes.restart?.service == 'web'" }
`
			docs, warns, err := model.ConvertV1([]byte(doc))
			if err != nil || (len(warns) == 1) != c.warn || len(warns) > 1 {
				t.Fatalf("convert: %v %q", err, warns)
			}
			nodes := docs[0].(*model.Policy).Spec.Nodes
			if got := nodes[1].Props["message"]; got != c.want {
				t.Fatalf("message = %q, want %q", got, c.want)
			}
			if got := nodes[2].Props["condition"]; got != "nodes.restart?.resource == 'web'" {
				t.Fatalf("condition = %q", got)
			}
		})
	}
}

func TestConvertAddsImpactAcknowledgement(t *testing.T) {
	doc := `apiVersion: lcm/v1
kind: Policy
metadata: { name: p }
spec:
  nodes:
    - { id: stop, type: lifecycle.stop, props: { service: web }, next: { out: [start] } }
    - { id: start, type: lifecycle.start, props: { service: web }, next: { out: [deploy] } }
    - { id: deploy, type: lifecycle.deploy, props: { service: web, acknowledgeImpact: false }, next: { out: [job] } }
    - { id: job, type: lifecycle.run-job, props: { service: web, job: reindex } }
`
	docs, warns, err := model.ConvertV1([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ack := map[string]any{}
	for _, n := range docs[0].(*model.Policy).Spec.Nodes {
		ack[n.ID] = n.Props["acknowledgeImpact"]
	}
	if ack["stop"] != true || ack["start"] != nil || ack["deploy"] != false || ack["job"] != true {
		t.Fatalf("acknowledgeImpact = %v", ack)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "Policy p: added acknowledgeImpact: true to lifecycle nodes stop, job") {
		t.Fatalf("warnings = %q", warns)
	}
	// A converted document converts to itself.
	b, _ := model.Encode(docs[0])
	again, warns, err := model.ConvertV1(b)
	if err != nil || len(warns) != 0 {
		t.Fatalf("v2 again: %v %q", err, warns)
	}
	if a, _ := model.Encode(again[0]); string(a) != string(b) {
		t.Fatalf("v2 changed:\n%s\n%s", a, b)
	}
}

func TestConvertLockNames(t *testing.T) {
	doc := `apiVersion: lcm/v1
kind: Policy
metadata: { name: p }
spec:
  params: { target: web }
  nodes:
    - { id: a, type: lock, props: { name: "service:web" }, next: { out: [b] } }
    - { id: b, type: lock, props: { name: "service:{{ params.target }}" }, next: { out: [c] } }
    - { id: c, type: lock, props: { name: "{{ 'service:' + params.target }}" }, next: { out: [d] } }
    - { id: d, type: lock, props: { name: "host:db1" }, next: { out: [e] } }
    - { id: e, type: unlock, props: { name: "service:web" } }
`
	docs, warns, err := model.ConvertV1([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]any{}
	for _, n := range docs[0].(*model.Policy).Spec.Nodes {
		names[n.ID] = n.Props["name"]
	}
	if names["a"] != "resource:web" || names["b"] != "resource:{{ params.target }}" || names["c"] != "{{ 'service:' + params.target }}" ||
		names["d"] != "host:db1" || names["e"] != "resource:web" {
		t.Fatalf("lock names = %v", names)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "spec.nodes[2].props.name") {
		t.Fatalf("warnings = %q", warns)
	}
}
