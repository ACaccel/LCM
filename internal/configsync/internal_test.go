package configsync

import (
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

func TestUnifiedDiff(t *testing.T) {
	if unifiedDiff("x", "x", "a\nb\n", "a\nb\n") != "" {
		t.Fatal("equal texts produced a diff")
	}
	var a, b []string
	for i := 1; i <= 30; i++ {
		a = append(a, "line"+string(rune('A'+i%26))+strings.Repeat("x", i))
	}
	b = append(b, a...)
	b[2] = "changed"
	b[27] = "changed too"
	b = append(b[:10], append([]string{"inserted"}, b[10:]...)...)
	got := unifiedDiff("S/x", "S/x", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	if !strings.HasPrefix(got, "--- a/S/x\n+++ b/S/x\n@@ -1,6 +1,6 @@\n") {
		t.Fatalf("first hunk header wrong:\n%s", got)
	}
	if strings.Count(got, "@@ -") != 3 {
		t.Fatalf("want three separate hunks:\n%s", got)
	}
	for _, want := range []string{"-" + a[2], "+changed\n", "+inserted\n", "-" + a[27], "+changed too\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff lacks %q:\n%s", want, got)
		}
	}

	// A diff that is applied to the old text gives the new one.
	if applied := applyUnified(t, strings.Join(a, "\n")+"\n", got); applied != strings.Join(b, "\n")+"\n" {
		t.Fatalf("applying the diff gives:\n%s", applied)
	}

	if d := unifiedDiff("n", "n", "", "k: 1\n"); d != "--- /dev/null\n+++ b/n\n@@ -0,0 +1 @@\n+k: 1\n" {
		t.Fatalf("creation diff = %q", d)
	}
	if d := unifiedDiff("n", "n", "k: 1\n", ""); d != "--- a/n\n+++ /dev/null\n@@ -1 +0,0 @@\n-k: 1\n" {
		t.Fatalf("deletion diff = %q", d)
	}
}

// applyUnified applies a diff made by unifiedDiff, to check hunk positions.
func applyUnified(t *testing.T, old, diff string) string {
	t.Helper()
	lines := splitLines(old)
	var out []string
	pos := 0
	for _, l := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "):
		case strings.HasPrefix(l, "@@ "):
			var start int
			rest := strings.TrimPrefix(l, "@@ -")
			num, _, _ := strings.Cut(rest, " ")
			num, _, _ = strings.Cut(num, ",")
			start = atoi(t, num)
			if !strings.Contains(l, ",0 +") && !strings.HasPrefix(num, "0") {
				start--
			}
			out = append(out, lines[pos:start]...)
			pos = start
		case l[0] == ' ':
			out = append(out, l[1:])
			pos++
		case l[0] == '-':
			pos++
		case l[0] == '+':
			out = append(out, l[1:])
		}
	}
	out = append(out, lines[pos:]...)
	return strings.Join(out, "\n") + "\n"
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestCanonicalYAMLRoundTrip(t *testing.T) {
	docs := []string{
		`apiVersion: lcm/v1
kind: Policy
metadata: { name: tricky, labels: { "true": "yes", n: "08" } }
spec:
  params: { text: "line1\nline2", empty: "", flag: "true", num: "12", list: [a, "b: c", "- d", "#e"], nested: { x: null, y: 1.5, z: [] } }
  nodes:
    - { id: start, type: trigger.manual, next: { out: [echo] } }
    - { id: echo, type: data.set, props: { values: { got: "{{ trigger.name }}", q: "it's \"quoted\"", u: "caf\u00e9 \u2603" } } }
`,
		"apiVersion: lcm/v1\nkind: Service\nmetadata: { name: web }\nspec:\n  runtime: { type: command, start: s, stop: t, restart: r, status: q }\n  jobs:\n    j: { command: \"echo 'a'\\nexit 1\", timeout: 90s }\n",
	}
	for _, text := range docs {
		doc, err := model.Decode([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		canon, err := model.Encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		y, err := canonicalYAML(canon)
		if err != nil {
			t.Fatal(err)
		}
		back, err := model.Decode(y)
		if err != nil {
			t.Fatalf("canonical YAML does not decode: %v\n%s", err, y)
		}
		canon2, _ := model.Encode(back)
		if string(canon) != string(canon2) {
			t.Fatalf("round trip changed the document:\n%s\n%s\nvia\n%s", canon, canon2, y)
		}
		if strings.Contains(string(y), ": {") || strings.Contains(string(y), `"kind"`) {
			t.Fatalf("output is not block style:\n%s", y)
		}
		if !strings.HasPrefix(string(y), "apiVersion: lcm/v1\nkind: ") {
			t.Fatalf("unexpected field order:\n%s", y)
		}
	}
}
