package hostsetup

import (
	"strings"
	"testing"
	"time"
)

func TestUpgradeRefusesFilesOfForeignOwners(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(u *upTest)
	}{
		{"database", func(u *upTest) { u.fs.file(DatabaseFile, "db-16", 0o600, 4242, 4242) }},
		{"wal", func(u *upTest) { u.fs.file(DatabaseFile+"-wal", "wal", 0o600, 0, 0) }},
		{"binary", func(u *upTest) { u.fs.file(Binary, "old-binary", 0o755, 990, 990) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			tc.setup(u)
			_, _, err := u.upgrade(UpgradeOptions{})
			if err == nil || !strings.Contains(err.Error(), "is owned by user") {
				t.Fatalf("err = %v", err)
			}
			u.noWrites()
			u.noStop()
		})
	}
}

// The descriptor decides: a file swapped in after the preflight is refused
// by the copy even though the path looked right before.
func TestUpgradeBackupRefusesAnOpenedFileOfAnotherOwner(t *testing.T) {
	u := newUpTest(t)
	u.th.host.FS = descriptorSwapFS{memFS: u.fs, swap: descriptorSwapTree{mode: 0o600, uid: 777}}
	_, _, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "owned by user 777, not 990; nothing was copied") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if sets, _ := u.fs.ReadDir(BackupRoot); len(sets) > 0 {
		t.Errorf("partial set left: %v", sets)
	}
}

func TestUpgradeTreeCopyHasAHardByteBudget(t *testing.T) {
	u := newUpTest(t)
	// Files lcm adds after the preflight must not be copied without bound.
	u.r.hook = func(l string) error {
		if matches(l, "systemctl stop") {
			u.fs.file(DataDir+"/secrets/grown", strings.Repeat("x", 3<<20), 0o600, 990, 990)
		}
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "larger than the") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if sets, _ := u.fs.ReadDir(BackupRoot); len(sets) > 0 {
		t.Errorf("partial set left: %v", sets)
	}
	if u.fs.content(Binary) != "old-binary" || !u.r.active {
		t.Error("the host was not left as it was")
	}
}

func TestUpgradeChecksFreeSpaceBeforeEachFile(t *testing.T) {
	u := newUpTest(t)
	set := u.setName()
	u.free = func(p string) (uint64, error) {
		// Plenty for the first file of /etc/lcm, nothing for the next.
		if strings.Contains(p, "/upgrade-") && u.fs.get(set+"/etc-lcm/host.json") != nil {
			return 1 << 20, nil
		}
		return 1 << 40, nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "the backup stopped") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if u.fs.get(set) != nil {
		t.Error("partial set left")
	}
}

func TestUpgradeListenerMustBelongToTheUnit(t *testing.T) {
	cases := []struct {
		name string
		at   func(r *upRunner)
		want string
	}{
		{"port held by another process", func(r *upRunner) { r.foreignListener = true }, "does not belong to process"},
		{"another listener on the port", func(r *upRunner) { r.extraListener = true }, "does not belong to process"},
		{"process replaced while it settles", func(r *upRunner) { r.pidChangeAt = 3 }, "NRestarts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			u.r.onStart = func(n int) error {
				u.r.foreignListener, u.r.extraListener, u.r.pidChangeAt = false, false, 0
				if n == 1 {
					tc.at(u.r)
				}
				return nil
			}
			set := u.setName()
			_, out, err := u.upgrade(UpgradeOptions{})
			want := tc.want
			if want == "NRestarts" {
				want = "restarted by systemd"
			}
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), set) {
				t.Fatalf("err = %v, want %q\n%s", err, want, out)
			}
			u.restoredOld()
		})
	}
}

func TestUpgradeSettlesLongerThanRestartSec(t *testing.T) {
	u := newUpTest(t)
	var restartedAt time.Time
	u.r.hook = func(l string) error {
		if matches(l, "systemctl restart") {
			restartedAt = u.th.clock.Now()
		}
		return nil
	}
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The fake unit has RestartSec=5s.
	if waited := u.th.clock.Now().Sub(restartedAt); waited < 6*time.Second {
		t.Errorf("waited %s after the restart, want more than RestartSec", waited)
	}
}

func TestParseSystemdSpan(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"5s": 5 * time.Second, "100ms": 100 * time.Millisecond, "1min 30s": 90 * time.Second,
		"2h": 2 * time.Hour, "0": 0,
	} {
		if in == "0" {
			if _, ok := parseSystemdSpan(in); ok {
				t.Errorf("%q: a bare number is not a span", in)
			}
			continue
		}
		if got, ok := parseSystemdSpan(in); !ok || got != want {
			t.Errorf("%q = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "s", "5", "5 fortnights", "infinity"} {
		if _, ok := parseSystemdSpan(in); ok {
			t.Errorf("%q must not parse", in)
		}
	}
	if d := settleDelay(unitState{}); d != 6*time.Second {
		t.Errorf("default settle = %v", d)
	}
	if d := settleDelay(unitState{restartDelay: 30 * time.Second}); d != 31*time.Second {
		t.Errorf("settle = %v", d)
	}
}
