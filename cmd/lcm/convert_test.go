package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const convertService = `apiVersion: lcm/v1
kind: Service
metadata: { name: web }
spec:
  runtime: { type: systemd, unit: web.service }
  dependsOn: [db]
`

const convertPolicy = `apiVersion: lcm/v1
kind: Policy
metadata: { name: restart }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [act] } }
    - { id: act, type: lifecycle.restart, props: { service: "{{ service }}" } }
`

func TestConvertCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(convertService+"---\n"+convertPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	a, out, errb, _ := newApp(t, "apiVersion: lcm/v1\nkind: Machine\nmetadata: { name: ws }\n")
	r := runCLI(t, a, out, errb, "convert", "-f", dir, "-f", "-")
	if r.code != 0 {
		t.Fatalf("code %d: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		"apiVersion: lcm/v2\nkind: Resource\nmetadata:\n  name: web\nspec:\n  type: service\n  adapter: systemd\n",
		"  relations:\n    - type: dependsOn\n      target: db\n",
		"---\napiVersion: lcm/v2\nkind: Policy\n",
		"resource: '{{ service }}'",
		"---\napiVersion: lcm/v2\nkind: Resource\nmetadata:\n  name: ws\nspec:\n  type: machine\n  adapter: local\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "a.yaml: warning: Policy restart: spec.nodes[1].props.resource") {
		t.Fatalf("warnings = %s", r.stderr)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(convertService+"---\napiVersion: lcm/v1\nkind: Service\nmetadata: { name: Bad }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, a, out, errb, "convert", "-f", bad)
	if r.code != 3 || !strings.Contains(r.stderr, "bad.yaml: document 2:") || !strings.Contains(r.stdout, "name: web") {
		t.Fatalf("invalid input: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, a, out, errb, "convert"); r.code != 2 {
		t.Fatalf("missing -f: %d", r.code)
	}
}
