package deployhelper

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// TestRenderedUnitVerifies runs the host's systemd-analyze, when there is
// one, on the built-in template, so that a misspelled or unsupported
// property shows up here instead of on a host. Only the account and paths
// that do not exist on a test machine are replaced.
func TestRenderedUnitVerifies(t *testing.T) {
	if _, err := os.Stat(systemdAnalyze); err != nil {
		t.Skip("no systemd-analyze; the container end-to-end test verifies the unit")
	}
	unit := renderUnit("web", 3200, []string{"/bin/true", "--listen"}, "/opt/lcm/runtimes/node24/bin", "/var/backups/lcm")
	unit = strings.NewReplacer("User=web\n", "User=root\n", "Group=web\n", "Group=root\n",
		"WorkingDirectory=/opt/web/current\n", "WorkingDirectory=/\n", "EnvironmentFile=/etc/web/env\n", "EnvironmentFile=-/etc/web/env\n").Replace(unit)
	verifyUnit(t, "web.service", unit)

	// The properties of a build, as a unit of the same kind.
	req := Request{Op: OpBuild, Service: "web", Commit: commit1, Command: "make", TimeoutSec: 600, Runtimes: []string{"node24"}}
	builder := hostsetup.Account{Name: "lcmb-web", UID: 990, GID: 990}
	argv := buildArgs("lcm-build-web-x", "/var/lib/lcm-build/web/x", "/var/cache/lcm-build/web", builder, req,
		map[string]string{"assets": input}, "/var/backups/lcm")
	lines := []string{"[Service]", "Type=exec", "ExecStart=/bin/true"}
	for _, a := range argv {
		if p, ok := strings.CutPrefix(a, "--property="); ok {
			lines = append(lines, p)
		}
	}
	verifyUnit(t, "lcm-build-web-x.service", strings.Join(lines, "\n")+"\n")
}

func verifyUnit(t *testing.T, name, unit string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, systemdAnalyze, "verify", "--man=no", p).CombinedOutput() //nolint:gosec // a fixed program on a file the test wrote
	if err != nil {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, out)
	}
	for _, bad := range []string{"Unknown key", "Unknown section", "Failed to parse", "Invalid", "ignoring"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("systemd-analyze verify reports %q:\n%s", bad, out)
		}
	}
}
