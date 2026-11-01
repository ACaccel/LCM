package power

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxLedger = 16 << 20
const maxReceipts = 10000

// The ledger and lock must be provisioned by the administrator. Missing storage
// is never silently initialized: it could be a lost deduplication history.
type fileStore struct {
	root  *os.Root
	owner uint32
}
type ledger struct {
	Version  int                `json:"version"`
	Receipts map[string]Receipt `json:"receipts"`
}
type fileTx struct {
	store    *fileStore
	lock     *os.File
	data     ledger
	poisoned bool
}

func secureInfo(info os.FileInfo, owner uint32, dir bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("untrusted ownership or symlink")
	}
	if dir {
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return errors.New("insecure directory")
		}
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Nlink != 1 {
		return errors.New("insecure file")
	}
	return nil
}

// secureRoot checks every ancestor without following symlinks. Each ancestor
// must remain owned by root and unwritable by non-root users.
func secureRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("invalid trusted path")
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		info, err := root.Lstat(part)
		if err == nil {
			err = secureInfo(info, 0, true)
		}
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		next, err := root.OpenRoot(part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func secureFile(root *os.Root, name string, flags int, owner uint32) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := secureInfo(before, owner, false); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err == nil {
		err = secureInfo(after, owner, false)
	}
	if err == nil && !os.SameFile(before, after) {
		err = errors.New("file replaced")
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (s *fileStore) Lock(ctx context.Context) (ReceiptTx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := secureFile(s.root, "lock", os.O_RDWR, s.owner)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, err
	}
	tx := &fileTx{store: s, lock: lock}
	f, err := secureFile(s.root, "receipts.json", os.O_RDONLY, s.owner)
	if err == nil {
		err = decodeStrict(f, maxLedger, &tx.data)
		_ = f.Close()
	}
	if err == nil {
		err = tx.validate()
	}
	if err != nil {
		_ = tx.Close()
		return nil, err
	}
	return tx, nil
}

func (t *fileTx) validate() error {
	if t.data.Version != Version || t.data.Receipts == nil || len(t.data.Receipts) > maxReceipts {
		return errors.New("invalid ledger")
	}
	for key, r := range t.data.Receipts {
		if key != receiptKey(r.Binding.ControllerID, r.Binding.OperationID) || r.Binding.validate() != nil || r.UpdatedAt <= 0 {
			return errors.New("corrupt receipt")
		}
		switch r.State {
		case Accepted, Submitting, Failed, Indeterminate:
		default:
			return errors.New("invalid receipt state")
		}
		if r.OSRequested && r.State != Accepted {
			return errors.New("invalid OS evidence")
		}
	}
	return nil
}
func receiptKey(controllerID, operationID string) string { return controllerID + "/" + operationID }
func (t *fileTx) Get(controllerID, operationID string) (Receipt, bool, error) {
	r, ok := t.data.Receipts[receiptKey(controllerID, operationID)]
	return r, ok, nil
}
func (t *fileTx) Put(r Receipt) error {
	if t.poisoned {
		return errors.New("transaction requires recovery")
	}
	key := receiptKey(r.Binding.ControllerID, r.Binding.OperationID)
	old, found := t.data.Receipts[key]
	if found && old.Binding != r.Binding {
		return errors.New("immutable binding changed")
	}
	if !found && len(t.data.Receipts) >= maxReceipts {
		return errors.New("receipt capacity reached")
	}
	t.data.Receipts[key] = r
	if err := t.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(t.data)
	if err != nil {
		return err
	}
	if len(raw) > maxLedger {
		return errors.New("ledger capacity reached")
	}
	t.poisoned = true
	name := ".receipt-" + rand.Text()
	f, err := t.store.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = t.store.root.Remove(name) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := t.store.root.Rename(name, "receipts.json"); err != nil {
		return err
	}
	dir, err := t.store.root.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return err
	}
	t.poisoned = false
	return nil
}
func (t *fileTx) Close() error { return t.lock.Close() }

func readConfig(root *os.Root, dst any) error {
	f, err := secureFile(root, "machine-power.json", os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return decodeStrict(f, MaxMessage, dst)
}
