package power

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

func diskFixture(t *testing.T) (*fileStore, string) {
	t.Helper()
	dir := t.TempDir()
	for name, raw := range map[string]string{"lock": "", "receipts.json": `{"version":1,"receipts":{}}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	return &fileStore{root: root, owner: info.Sys().(*syscall.Stat_t).Uid}, dir
}
func TestFileStoreDurableReloadAndLocking(t *testing.T) {
	store, _ := diskFixture(t)
	ctx := context.Background()
	tx, err := store.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(ctx); err == nil {
		t.Fatal("parallel lock accepted")
	}
	r := Receipt{Binding: binding(), State: Submitting, UpdatedAt: 1000000}
	if err := tx.Put(r); err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err = store.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Close() }()
	got, found, err := tx.Get(controller, operation)
	if err != nil || !found || got != r {
		t.Fatalf("reload %+v %v %v", got, found, err)
	}
	other := r
	other.Binding.Action = GracefulShutdown
	other.Binding.RequestHash = Hash(other.Binding)
	if err := tx.Put(other); err == nil {
		t.Fatal("binding mutation accepted")
	}
	dir, err := store.root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".receipt-") {
			t.Fatal("temporary receipt leaked")
		}
	}
}
func TestStorageFailClosed(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "hardlink", "mode", "owner", "corrupt", "wrong-version", "invalid-receipt", "lock-symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, dir := diskFixture(t)
			path := filepath.Join(dir, "receipts.json")
			var err error
			switch kind {
			case "missing":
				err = os.Remove(path)
			case "symlink":
				err = os.Rename(path, filepath.Join(dir, "real"))
				if err == nil {
					err = os.Symlink("real", path)
				}
			case "hardlink":
				err = os.Link(path, filepath.Join(dir, "linked"))
			case "mode":
				err = os.Chmod(path, 0644) //nolint:gosec // Deliberately insecure fixture must be rejected.
			case "owner":
				store.owner++
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "wrong-version":
				err = os.WriteFile(path, []byte(`{"version":2,"receipts":{}}`), 0600)
			case "invalid-receipt":
				err = os.WriteFile(path, []byte(`{"version":1,"receipts":{"bad":{}}}`), 0600)
			case "lock-symlink":
				err = os.Remove(filepath.Join(dir, "lock"))
				if err == nil {
					err = os.Symlink("receipts.json", filepath.Join(dir, "lock"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if tx, err := store.Lock(context.Background()); err == nil {
				_ = tx.Close()
				t.Fatal("unsafe storage accepted")
			}
		})
	}
}
func TestTrustedPathsAndPrivilege(t *testing.T) {
	if root, err := secureRoot("/tmp"); err == nil {
		_ = root.Close()
		t.Fatal("world-writable root accepted")
	}
	if root, err := secureRoot("relative"); err == nil {
		_ = root.Close()
		t.Fatal("relative root accepted")
	}
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t).Uid
	if secureInfo(info, owner+1, true) == nil {
		t.Fatal("wrong owner accepted")
	}
	if os.Geteuid() != 0 {
		if err := ServeLocal(context.Background(), strings.NewReader("{}"), &strings.Builder{}); err == nil {
			t.Fatal("non-root helper accepted")
		}
	}
}

func TestDiskReceiptRecoveryAcrossHelperProcesses(t *testing.T) {
	store, _ := diskFixture(t)
	sys := &fakeSystem{boot: boot, crash: true}
	h := &Helper{ControllerID: controller, Store: store, System: sys, Clock: clock.NewFake(time.UnixMilli(1000000))}
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected process loss")
			}
		}()
		h.Handle(context.Background(), request(Submit))
	}()
	restarted := *h
	restarted.Store = &fileStore{root: store.root, owner: store.owner}
	sys.boot = controller
	mustReceipt(t, restarted.Handle(context.Background(), request(Query)), Indeterminate, false)
	mustReceipt(t, restarted.Handle(context.Background(), request(Submit)), Indeterminate, false)
	if sys.calls != 1 {
		t.Fatal("persisted handoff repeated across boot")
	}
}
