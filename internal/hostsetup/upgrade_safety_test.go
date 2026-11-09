package hostsetup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
)

// existFS makes the creation of one directory lose a race: the name appears
// between the check and the Mkdir.
type existFS struct {
	*memFS
	name string
}

func (e existFS) Mkdir(p string, perm fs.FileMode, uid, gid int) error {
	if p == e.name {
		e.dir(p, 0o700, 0, 0)
		e.file(p+"/precious", "someone else's backup", 0o600, 0, 0)
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	return e.memFS.Mkdir(p, perm, uid, gid)
}

func TestUpgradeNeverRemovesASetItDidNotCreate(t *testing.T) {
	t.Run("exists before the backup", func(t *testing.T) {
		u := newUpTest(t)
		set := u.setName()
		u.fs.dir(set, 0o700, 0, 0)
		u.fs.file(set+"/precious", "an earlier backup", 0o600, 0, 0)
		_, out, err := u.upgrade(UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if u.fs.content(set+"/precious") != "an earlier backup" {
			t.Error("the existing set was changed")
		}
		if u.fs.content(Binary) != "old-binary" {
			t.Error("the binary was replaced")
		}
	})
	t.Run("appears during the backup", func(t *testing.T) {
		u := newUpTest(t)
		set := u.setName()
		u.th.host.FS = existFS{memFS: u.fs, name: set}
		_, out, err := u.upgrade(UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if u.fs.content(set+"/precious") != "someone else's backup" {
			t.Error("the set that won the race was removed")
		}
	})
}

func TestUpgradeSpaceCountsEtcAndSecrets(t *testing.T) {
	u := newUpTest(t)
	u.free = func(string) (uint64, error) { return 20 << 20, nil }
	if _, out, err := u.upgrade(UpgradeOptions{Check: true}); err != nil {
		t.Fatalf("control: %v\n%s", err, out)
	}
	u.fs.file(DataDir+"/secrets/big", strings.Repeat("x", 8<<20), 0o600, 990, 990)
	_, _, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err = %v", err)
	}
	u.noStop()
}

func TestUpgradeBackupStopsWhenSpaceRunsOut(t *testing.T) {
	u := newUpTest(t)
	u.free = func(p string) (uint64, error) {
		if strings.Contains(p, "/upgrade-") {
			return 1 << 20, nil
		}
		return 1 << 40, nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "the backup stopped") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if sets, _ := u.fs.ReadDir(BackupRoot); len(sets) > 0 {
		t.Errorf("partial set left: %v", sets)
	}
	if u.fs.content(Binary) != "old-binary" || !u.r.active {
		t.Error("the host was not left as it was")
	}
}

func TestUpgradeHoldsTheSelfInstallLock(t *testing.T) {
	t.Run("held from preflight to the end", func(t *testing.T) {
		u := newUpTest(t)
		var heldAtStop, heldAtRestart bool
		u.r.hook = func(l string) error {
			switch {
			case matches(l, "systemctl stop"):
				heldAtStop = u.locks == 1
			case matches(l, "systemctl restart"):
				heldAtRestart = u.locks == 1
			}
			return nil
		}
		if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !heldAtStop || !heldAtRestart || u.locks != 0 {
			t.Errorf("held at stop %v, at restart %v, left %d", heldAtStop, heldAtRestart, u.locks)
		}
	})
	t.Run("released after a failure", func(t *testing.T) {
		u := newUpTest(t)
		u.r.onStart = func(n int) error {
			if n == 1 {
				return exit(1)
			}
			return nil
		}
		if _, _, err := u.upgrade(UpgradeOptions{}); err == nil {
			t.Fatal("expected an error")
		}
		if u.locks != 0 {
			t.Errorf("locks left = %d", u.locks)
		}
	})
	t.Run("refused while self-install holds it", func(t *testing.T) {
		u := newUpTest(t)
		u.lock = func() (func(), error) { return nil, errors.New("a self-install holds the lock") }
		_, _, err := u.upgrade(UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "self-install holds") || !strings.Contains(err.Error(), "run the command again") {
			t.Fatalf("err = %v", err)
		}
		u.noWrites()
		u.noStop()
	})
	t.Run("check mode does not lock", func(t *testing.T) {
		u := newUpTest(t)
		u.lock = func() (func(), error) { return nil, errors.New("must not be called") }
		if _, out, err := u.upgrade(UpgradeOptions{Check: true}); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
}

// descriptorSwapTree reports another file than the one that was looked at, or the
// metadata of the opened descriptor.
type descriptorSwapTree struct {
	Tree
	ino  uint64
	mode fs.FileMode
	uid  int
}

func (s descriptorSwapTree) Open(rel string) (io.ReadCloser, Info, error) {
	r, info, err := s.Tree.Open(rel)
	if err == nil && rel == "lcm.db" {
		info.Ino += s.ino
		if s.mode != 0 {
			info.Mode = s.mode
			info.UID = s.uid
		}
	}
	return r, info, err
}

type descriptorSwapFS struct {
	*memFS
	swap descriptorSwapTree
}

func (f descriptorSwapFS) OpenTree(root string) (Tree, error) {
	t, err := f.memFS.OpenTree(root)
	if err != nil || root != DataDir {
		return t, err
	}
	s := f.swap
	s.Tree = t
	return s, nil
}

func TestUpgradeBackupFileMustBeTheFileThatWasChecked(t *testing.T) {
	u := newUpTest(t)
	u.th.host.FS = descriptorSwapFS{memFS: u.fs, swap: descriptorSwapTree{ino: 1}}
	_, _, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("err = %v", err)
	}
	if sets, _ := u.fs.ReadDir(BackupRoot); len(sets) > 0 {
		t.Errorf("partial set left: %v", sets)
	}
}

func TestUpgradeBackupTakesMetadataFromTheOpenedFile(t *testing.T) {
	u := newUpTest(t)
	u.th.host.FS = descriptorSwapFS{memFS: u.fs, swap: descriptorSwapTree{mode: 0o640, uid: 777}}
	if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(u.fs.content(u.setName()+"/manifest.json"), `"uid": 777`) {
		t.Errorf("the manifest does not carry the descriptor's owner:\n%s", u.fs.content(u.setName()+"/manifest.json"))
	}
}

// The health answer must come from the unit's own process.
func TestUpgradeChecksTheUnitBehindTheHealthAnswer(t *testing.T) {
	cases := []struct {
		name string
		// at is what happens when the new binary starts (n == 1).
		at   func(r *upRunner)
		want string
	}{
		{"unit not active", func(r *upRunner) { r.notActive = true }, "not active"},
		{"process outside the unit's cgroup", func(r *upRunner) { r.badCgroup = true }, "not in the control group"},
		{"main process keeps changing", func(r *upRunner) { r.churnPID = true }, "changed its main process"},
		{"restarted by systemd", func(r *upRunner) { r.restarts++ }, "restarted by systemd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpTest(t)
			u.advanceClock()
			u.r.onStart = func(n int) error {
				// The rollback's own start (n == 2) is a healthy unit again.
				u.r.notActive, u.r.badCgroup, u.r.churnPID = false, false, false
				if n == 1 {
					tc.at(u.r)
				}
				return nil
			}
			set := u.setName()
			_, out, err := u.upgrade(UpgradeOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), set) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			u.restoredOld()
		})
	}
}

func TestUpgradeChecksTheUnitAfterTheNormalRestartToo(t *testing.T) {
	u := newUpTest(t)
	u.advanceClock()
	u.r.onStart = func(n int) error {
		u.r.badCgroup = n == 2
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "not in the control group") || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	u.restoredOld()
}

func TestUpgradeRollbackChecksTheOldUnit(t *testing.T) {
	u := newUpTest(t)
	u.advanceClock()
	u.r.onStart = func(n int) error {
		switch n {
		case 1:
			return exit(1)
		case 2:
			u.r.badCgroup = true
		}
		return nil
	}
	_, out, err := u.upgrade(UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILED") || !strings.Contains(err.Error(), "not in the control group") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// The last assignment in the unit's environment is the effective one.
func TestUpgradeUsesTheEffectiveAutomationSetting(t *testing.T) {
	t.Run("off overridden by a later on", func(t *testing.T) {
		u := newUpTest(t)
		u.r.envOverride = "A=1 LCM_AUTOMATION=off LCM_AUTOMATION=on"
		_, _, err := u.upgrade(UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "refusing to start LCM with automation on") {
			t.Fatalf("err = %v", err)
		}
		if got := fmt.Sprint(u.r.startedOff); got != "[false]" {
			t.Errorf("starts = %s, want only the rollback's", got)
		}
	})
	t.Run("on overridden by a later off", func(t *testing.T) {
		u := newUpTest(t)
		u.r.onStart = func(n int) error {
			if n == 1 {
				u.r.envOverride = `LCM_AUTOMATION=on "LCM_AUTOMATION=off"`
			}
			return nil
		}
		_, _, err := u.upgrade(UpgradeOptions{})
		if err == nil || !strings.Contains(err.Error(), "refusing to leave automation off") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("on after an earlier off passes", func(t *testing.T) {
		u := newUpTest(t)
		u.r.onStart = func(n int) error {
			if n == 1 {
				u.r.envOverride = "LCM_AUTOMATION=off LCM_AUTOMATION=on"
			}
			return nil
		}
		if _, out, err := u.upgrade(UpgradeOptions{}); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
}
