package hostsetup

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/upgrademarker"
)

func TestUpgradeLeavesAMarkerForTheServer(t *testing.T) {
	u := newUpTest(t)
	_, out, err := u.upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// After the validation start, before the start that runs normally.
	u.order("run systemctl start lcm.service", "write "+UpgradeMarker, "run systemctl restart lcm.service")
	info, err := u.fs.Lstat(UpgradeMarker)
	if err != nil || !info.IsRegular() || info.UID != 990 || info.GID != 990 || info.Mode.Perm() != 0o600 {
		t.Fatalf("marker = %+v %v, want a regular file of lcm, mode 0600", info, err)
	}
	m, err := upgrademarker.Decode([]byte(u.fs.content(UpgradeMarker)))
	if err != nil {
		t.Fatal(err)
	}
	want := upgrademarker.Marker{Format: 1, At: u.th.clock.Now().UTC().Truncate(time.Second),
		From: upgrademarker.Build{Version: "v1", Commit: "aaaaaaa", Schema: 16},
		To:   upgrademarker.Build{Version: "v2", Commit: "bbbbbbb", Schema: 17}}
	// The clock moves on while the upgrade waits, so only the builds are exact.
	if m.From != want.From || m.To != want.To || m.At.Before(want.At.Add(-time.Hour)) {
		t.Fatalf("marker = %+v, want builds %+v -> %+v", m, want.From, want.To)
	}
	if !strings.Contains(out, "leave "+UpgradeMarker) {
		t.Errorf("output does not mention the marker:\n%s", out)
	}
}

func TestUpgradeCheckLeavesNoMarker(t *testing.T) {
	u := newUpTest(t)
	_, out, err := u.upgrade(UpgradeOptions{Check: true})
	if err != nil || !strings.Contains(out, "would change: leave "+UpgradeMarker) {
		t.Fatalf("%v\n%s", err, out)
	}
	if u.fs.get(UpgradeMarker) != nil {
		t.Error("check mode wrote the marker")
	}
}

// A marker for an upgrade that was rolled back must not stay: the restored
// database knows nothing of it.
func TestUpgradeRollbackRemovesTheMarker(t *testing.T) {
	u := newUpTest(t)
	u.r.onStart = func(n int) error {
		if n == 2 {
			return exit(1)
		}
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if u.fs.get(UpgradeMarker) != nil {
		t.Error("the marker of a rolled back upgrade was left behind")
	}
	u.order("write "+UpgradeMarker, "remove "+UpgradeMarker)
	u.restoredOld()
}

// A failure before the marker is written never touches the path.
func TestUpgradeFailureBeforeTheMarkerLeavesNoTrace(t *testing.T) {
	u := newUpTest(t)
	u.fs.file(UpgradeMarker, "not ours", 0o600, 990, 990)
	u.r.schemaAfterStart = 16
	if _, out, err := u.upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if got := u.fs.content(UpgradeMarker); got != "not ours" {
		t.Errorf("a file at the marker path was changed: %q", got)
	}
}

// The marker is bookkeeping: when it cannot be written the upgrade still
// succeeds and the operator gets the command that records the deployment.
func TestUpgradeSucceedsWhenTheMarkerCannotBeWritten(t *testing.T) {
	u := newUpTest(t)
	u.th.host.FS = failWriteFS{memFS: u.fs, fail: func(p string) error {
		if p == UpgradeMarker {
			return errors.New("no space left")
		}
		return nil
	}}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatalf("a marker that cannot be written failed the upgrade: %v\n%s", err, out)
	}
	for _, want := range []string{"warning: " + "leave " + UpgradeMarker, "no space left", "lcm resource action lcm deploy --version bbbbbbb", "is upgraded from"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if u.fs.content(Binary) != "new-binary" {
		t.Error("the upgrade did not install the new binary")
	}
}

// lcm owns the data directory and can plant anything at the marker's name:
// the root write replaces the entry and never follows it.
func TestUpgradeMarkerReplacesAPlantedLink(t *testing.T) {
	u := newUpTest(t)
	u.fs.file("/etc/shadow", "root-secret", 0o600, 0, 0)
	u.fs.link(UpgradeMarker, "/etc/shadow", 990, 990)
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := u.fs.content("/etc/shadow"); got != "root-secret" {
		t.Errorf("the write followed the link: /etc/shadow = %q", got)
	}
	info, err := u.fs.Lstat(UpgradeMarker)
	if err != nil || !info.IsRegular() || info.UID != 990 {
		t.Fatalf("marker = %+v %v, want a regular file of lcm", info, err)
	}
	if _, err := upgrademarker.Decode([]byte(u.fs.content(UpgradeMarker))); err != nil {
		t.Fatal(err)
	}
}

// A data directory that is not lcm's is not written to.
func TestUpgradeMarkerRefusesForeignDataDirectory(t *testing.T) {
	u := newUpTest(t)
	u.r.onStart = func(n int) error {
		if n != 1 {
			return nil
		}
		info, err := u.fs.Lstat(DataDir)
		if err != nil {
			return err
		}
		return u.fs.Chown(DataDir, info, 0, 0)
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := u.fs.Lstat(UpgradeMarker); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("marker written into a directory lcm does not own: %v", err)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "not a directory owned by lcm") {
		t.Errorf("output lacks the warning:\n%s", out)
	}
}
