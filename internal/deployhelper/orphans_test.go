package deployhelper

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// A single unit is not activated while a file named like an instance unit
// of the service was not written by the helper, and every orphaned
// instance unit is found before the switch.
func TestSingleActivateScansInstanceUnits(t *testing.T) {
	e := newEnv(t)
	_, rel1 := e.deploy(commit1)
	marked := hostsetup.ServiceUnitMarker + "\n[Unit]\n"
	e.write("/etc/systemd/system/web@a.service", marked, 0o644)
	e.write("/etc/systemd/system/web@b.service", "[Unit]\n", 0o644)
	e.write("/etc/systemd/system/web@c.service", marked, 0o644)
	e.refused(Request{Op: OpActivate, Service: "web", ReleaseID: rel1, Start: []string{"index.js"}, Runtime: "node24"},
		CodeRefused, "/etc/systemd/system/web@b.service exists and was not written by the deploy helper")
	if err := os.Remove(e.real("/etc/systemd/system/web@b.service")); err != nil {
		t.Fatal(err)
	}
	n := len(e.systemctlCalls())
	r := e.ok(Request{Op: OpActivate, Service: "web", ReleaseID: rel1, Start: []string{"index.js"}, Runtime: "node24"})
	if !slices.Contains(e.systemctlCalls()[n:], "disable --now --quiet web@a.service web@c.service") || !slices.Equal(r.Activate.Removed, []string{"a", "c"}) {
		t.Fatalf("systemctl %q, removed %v", e.systemctlCalls()[n:], r.Activate.Removed)
	}
	if e.exists("/etc/systemd/system/web@a.service") || e.exists("/etc/systemd/system/web@c.service") {
		t.Fatal("orphans left")
	}
}

func TestInstancesSharedEnvAndMigration(t *testing.T) {
	e, sd := instancesEnv(t)
	rel := e.installed(commit1)
	e.write("/etc/web/env", "LCM_INSTANCE=a\n", 0o640)
	e.refused(activateInstances(rel, instances("a")), CodeRefused, "/etc/web/env sets LCM_INSTANCE")
	e.write("/etc/web/env", "SECRET=1\n", 0o640)

	// The migration of a service with instances has no port.
	req := activateInstances(rel, instances("a", "b", "c"))
	req.Migrate = []string{"node", "migrate.js"}
	e.ok(req)
	argv := workerArgv(e, "lcm-migrate-")
	if argv == nil || slices.ContainsFunc(argv, func(a string) bool {
		return strings.HasPrefix(a, "--setenv=PORT") || strings.HasPrefix(a, "--setenv=HOST")
	}) {
		t.Fatalf("migration argv %q", argv)
	}
	if slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--property=SocketBindAllow") }) {
		t.Fatalf("migration may bind a port: %q", argv)
	}

	// A restart that fails still removes the units of instances no longer
	// requested, which were stopped before it.
	rel2 := e.installed(commit2)
	sd.restartFail = "web@a.service"
	r := e.refused(activateInstances(rel2, instances("a", "b")), CodeFailed, "systemctl restart exited with status 1")
	if e.exists(hostsetup.InstanceUnit("web", "c")) || !slices.Equal(r.Activate.Removed, []string{"c"}) {
		t.Fatalf("orphan c after a failed restart: %+v", r.Activate)
	}
}
