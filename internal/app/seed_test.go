package app_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/templates"
)

// seedScript runs bash with deploy/seed.sh sourced (its main function does not
// run) and the given commands, returning the exit code and combined output.
func seedScript(t *testing.T, env []string, commands string) (int, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	script, err := filepath.Abs(filepath.Join(deployDir, "seed.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1" && shift && `+commands, "bash", script) //nolint:gosec // fixed test fixture
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("run bash: %v", err)
	return 0, ""
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil { //nolint:gosec // test fixture, possibly executable
		t.Fatal(err)
	}
	return p
}

const printInventory = `parse_inventory "$INV"; for i in "${!inv_name[@]}"; do printf '%s|%s|%s|%s\n' "${inv_kind[i]}" "${inv_name[i]}" "${inv_target[i]}" "${inv_url[i]}"; done`

func TestSeedInventoryParsing(t *testing.T) {
	dir := t.TempDir()
	valid := writeFile(t, dir, "valid", `# monitoring-only services
service web ^web(-.+)?\.service$ http://127.0.0.1:8080/health
  service  worker   ^worker@.+\.service$   # trailing comment

container cache cache-1 https://cache.example.test/ping?x=1

`, 0o600)
	code, out := seedScript(t, []string{"INV=" + valid}, printInventory)
	want := `service|web|^web(-.+)?\.service$|http://127.0.0.1:8080/health
service|worker|^worker@.+\.service$|
container|cache|cache-1|https://cache.example.test/ping?x=1
`
	if code != 0 || out != want {
		t.Fatalf("exit %d, output:\n%s\nwant:\n%s", code, out, want)
	}

	example := filepath.Join(deployDir, "seed-inventory.example")
	if code, out := seedScript(t, []string{"INV=" + example}, printInventory); code != 0 || !strings.Contains(out, "service|web|") {
		t.Fatalf("example inventory: exit %d\n%s", code, out)
	}

	empty := writeFile(t, dir, "empty", "# nothing yet\n\n", 0o600)
	if code, out := seedScript(t, []string{"INV=" + empty}, printInventory); code != 0 || out != "" {
		t.Fatalf("empty inventory: exit %d %q", code, out)
	}

	bad := []struct{ name, line, msg string }{
		{"unknown kind", "unit web ^web$", "unknown kind 'unit'"},
		{"too few fields", "service web", "want 'service NAME TARGET"},
		{"too many fields", "service web ^web$ http://127.0.0.1/ extra", "want 'service NAME TARGET"},
		{"uppercase name", "service Web ^web$", "invalid name"},
		{"underscore name", "service my_web ^web$", "invalid name"},
		{"long name", "service " + strings.Repeat("a", 64) + " ^web$", "invalid name"},
		{"reserved name", "service lcm ^lcm$", "reserved"},
		{"bad regex", "service web ^(web$", "invalid unit regex"},
		{"bad container", "container web -bad", "invalid container name"},
		{"ftp url", "service web ^web$ ftp://127.0.0.1/", "invalid URL"},
		{"url credentials", "service web ^web$ http://user:pass@127.0.0.1/", "invalid URL"},
		{"url quote", `service web ^web$ http://127.0.0.1/"x`, "invalid URL"},
		{"duplicate", "service web ^web$\ncontainer web web", "duplicate name 'web'"},
	}
	for _, c := range bad {
		p := writeFile(t, dir, "bad", "service ok ^ok$\n"+c.line+"\n", 0o600)
		code, out := seedScript(t, []string{"INV=" + p}, printInventory)
		if code == 0 || !strings.Contains(out, c.msg) || !strings.Contains(out, "bad:") {
			t.Errorf("%s: exit %d, output %q, want failure containing %q with a file:line prefix", c.name, code, out, c.msg)
		}
	}

	if code, out := seedScript(t, []string{"INV=" + filepath.Join(dir, "missing")}, printInventory); code == 0 || !strings.Contains(out, "cannot read inventory") {
		t.Errorf("missing file: exit %d %q", code, out)
	}
}

func TestSeedRenderPolicy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(deployDir, "lcm-auto-deploy.yaml")
	dest := filepath.Join(dir, "policy.yaml")
	render := `render_policy "$SRC" "$DEST"`
	base := []string{"SRC=" + src, "DEST=" + dest}
	tracked := readDeploy(t, "lcm-auto-deploy.yaml")

	// Notifications are off unless both variables are set.
	for name, extra := range map[string][]string{
		"neither":  nil,
		"only url": {"LCM_SEED_NOTIFY_URL=http://127.0.0.1:4100/notifications/x"},
		"only key": {"LCM_SEED_NOTIFY_TOKEN_SECRET=relay-key"},
	} {
		if code, out := seedScript(t, append(base, extra...), render); code != 0 {
			t.Fatalf("%s: exit %d %s", name, code, out)
		}
		got, err := os.ReadFile(dest) //nolint:gosec // test path
		if err != nil || string(got) != string(tracked) {
			t.Fatalf("%s: policy changed (%v)\n%s", name, err, got)
		}
	}

	const url, secret = "http://127.0.0.1:4100/notifications/lcm-deploy", "relay-key"
	if code, out := seedScript(t, append(base, "LCM_SEED_NOTIFY_URL="+url, "LCM_SEED_NOTIFY_TOKEN_SECRET="+secret), render); code != 0 {
		t.Fatalf("both set: exit %d %s", code, out)
	}
	got, err := os.ReadFile(dest) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	tpl, ok := templates.Get("auto-deploy")
	if !ok {
		t.Fatal("no auto-deploy template")
	}
	params := map[string]any{}
	for k, v := range deployPolicyParams {
		params[k] = v
	}
	params["notifyUrl"], params["notifyTokenSecret"] = url, secret
	want, err := tpl.Render("lcm-auto-deploy", params)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("rendered policy differs from the template rendering with the konata parameters:\n%s", got)
	}

	for name, env := range map[string][]string{
		"credentials in url": {"LCM_SEED_NOTIFY_URL=http://u:p@127.0.0.1/x", "LCM_SEED_NOTIFY_TOKEN_SECRET=k"},
		"not http":           {"LCM_SEED_NOTIFY_URL=file:///etc/passwd", "LCM_SEED_NOTIFY_TOKEN_SECRET=k"},
		"bad secret name":    {"LCM_SEED_NOTIFY_URL=" + url, "LCM_SEED_NOTIFY_TOKEN_SECRET=../key"},
	} {
		if code, out := seedScript(t, append(base, env...), render); code == 0 {
			t.Errorf("%s: accepted (%s)", name, out)
		}
	}
}

// TestSeedRunsWithFakeTools runs seed.sh as a whole against stub tools: the
// inventory becomes one monitoring-only document, then the self-deploy service
// and the policy are applied; without an inventory only those two.
func TestSeedRunsWithFakeTools(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "git", "awk", "grep"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	applied := filepath.Join(dir, "applied")
	for _, d := range []string{bin, applied} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, bin, "lcm", `#!/bin/sh
case "$1 $2" in
"apply -f") n=$(ls "$APPLIED" | wc -l); cp "$3" "$APPLIED/$n.yaml" ;;
"get resources")
	if [ "$3" = lcm ]; then echo '{"service":{"deployedVersion":"abc"}}'; else echo resources; fi ;;
*) echo "unexpected lcm $*" >&2; exit 1 ;;
esac
`, 0o700)
	writeFile(t, bin, "systemctl", `#!/bin/sh
case "$1" in
list-units) printf 'web.service loaded active running Web\nweb-worker.service loaded active running Worker\nother.service loaded active running Other\n' ;;
show) if [ "$3" = LoadState ]; then echo loaded; else echo simple; fi ;;
esac
`, 0o700)
	writeFile(t, bin, "curl", "#!/bin/sh\ncase \"$*\" in *down.example*) exit 22 ;; esac\nexit 0\n", 0o700)
	writeFile(t, bin, "docker", "#!/bin/sh\nexit 1\n", 0o700)
	token := writeFile(t, dir, "token", "synthetic\n", 0o600)
	inv := writeFile(t, dir, "inventory", "service web ^web(-.+)?\\.service$ http://127.0.0.1:8080/health\nservice gone ^gone\\.service$\ncontainer cache cache http://down.example/\n", 0o600)

	script, err := filepath.Abs(filepath.Join(deployDir, "seed.sh"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(inventory string) string {
		t.Helper()
		if err := os.RemoveAll(applied); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(applied, 0o750); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", script) //nolint:gosec // script under test
		cmd.Env = []string{
			"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "APPLIED=" + applied,
			"LCM_BIN=" + filepath.Join(bin, "lcm"), "LCM_TOKEN_FILE=" + token, "LCM_SEED_INVENTORY=" + inventory,
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("seed.sh: %v\n%s", err, out)
		}
		return string(out)
	}
	read := func(n int) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(applied, fmt.Sprintf("%d.yaml", n))) //nolint:gosec // test path
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	out := run(inv)
	monitored := read(0)
	for _, want := range []string{"name: web\n", `unit: "web.service"`, `unit: "web-worker.service"`, `url: "http://127.0.0.1:8080/health"`, "adapter: external"} {
		if !strings.Contains(monitored, want) {
			t.Errorf("monitored document lacks %q:\n%s", want, monitored)
		}
	}
	for _, unwanted := range []string{"other.service", "name: gone", "name: cache"} {
		if strings.Contains(monitored, unwanted) {
			t.Errorf("monitored document has %q:\n%s", unwanted, monitored)
		}
	}
	if !strings.Contains(out, "gone: no units on this host") || !strings.Contains(out, "cache: no such container") {
		t.Errorf("output lacks the skipped entries:\n%s", out)
	}
	if got := read(2); got != string(readDeploy(t, "lcm-auto-deploy.yaml")) {
		t.Errorf("policy applied with notifications configured by default:\n%s", got)
	}

	out = run(filepath.Join(dir, "no-such-inventory"))
	if !strings.Contains(out, "none (no inventory") {
		t.Errorf("missing inventory not reported:\n%s", out)
	}
	if entries, _ := os.ReadDir(applied); len(entries) != 2 {
		t.Errorf("applied %d documents without an inventory, want the service and the policy", len(entries))
	}
}
