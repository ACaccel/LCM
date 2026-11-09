package deployhelper

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrBusy reports a service lock that another operation holds.
var ErrBusy = errors.New("another operation of this service is running")

// ledgerRetention is how long records of finished operations are kept.
const ledgerRetention = 90 * 24 * time.Hour

// maxRecord bounds one record file.
const maxRecord = MaxResponse + 8<<10

// record is one ledger entry.
type record struct {
	Version     int       `json:"version"`
	OperationID string    `json:"operationId"`
	Service     string    `json:"service"`
	Op          Op        `json:"op"`
	RequestHash string    `json:"requestHash"`
	State       State     `json:"state"`
	StartedAt   int64     `json:"startedAt"`
	FinishedAt  int64     `json:"finishedAt,omitempty"`
	Response    *Response `json:"response,omitempty"`
}

// Ledger is the helper's operation ledger: a directory only its owner can
// use, with ops/ for records and locks/ for flock files. It is reached
// through an os.Root, so no name inside it resolves elsewhere.
type Ledger struct {
	root  *os.Root
	owner int
}

// OpenLedger opens the ledger directory dir (a real path): it must exist,
// not be a symbolic link, belong to owner and have mode 0700. Missing
// subdirectories are created. Its ancestors are checked by the caller.
func OpenLedger(dir string, owner int) (*Ledger, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if err := checkPrivate(fi, owner, true); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	l := &Ledger{root: root, owner: owner}
	if after, err := root.Stat("."); err != nil || !os.SameFile(fi, after) {
		_ = root.Close()
		return nil, fmt.Errorf("%s changed while it was opened", dir)
	}
	for _, sub := range []string{"ops", "locks"} {
		if err := root.Mkdir(sub, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			_ = root.Close()
			return nil, err
		}
		fi, err := root.Lstat(sub)
		if err == nil {
			err = checkPrivate(fi, owner, true)
		}
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("%s/%s: %w", dir, sub, err)
		}
	}
	return l, nil
}

// Close releases the ledger.
func (l *Ledger) Close() error { return l.root.Close() }

// checkPrivate requires a directory of mode 0700, or a regular file of mode
// 0600 with one link, that owner owns.
func checkPrivate(fi os.FileInfo, owner int, dir bool) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok || int(st.Uid) != owner:
		return errors.New("not owned by the helper's owner")
	case dir && (!fi.IsDir() || fi.Mode().Perm() != 0o700):
		return errors.New("must be a directory with mode 0700")
	case !dir && (!fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || st.Nlink != 1):
		return errors.New("must be a regular file with mode 0600 and one link")
	}
	return nil
}

// lock takes the exclusive lock name; with wait false it fails with
// ErrBusy instead of waiting.
func (l *Ledger) lock(name string, wait bool) (func(), error) {
	f, err := l.root.OpenFile("locks/"+name, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = checkPrivate(fi, l.owner, false)
	}
	if err == nil {
		how := syscall.LOCK_EX
		if !wait {
			how |= syscall.LOCK_NB
		}
		err = syscall.Flock(int(f.Fd()), how) //nolint:gosec // descriptors fit in int
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = ErrBusy
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// Lock takes the lock of one service, or fails with ErrBusy.
func (l *Ledger) Lock(service string) (func(), error) { return l.lock(service, false) }

func recordName(id string) string { return "ops/" + id + ".json" }

// get reads the record of an operation.
func (l *Ledger) get(id string) (record, bool, error) {
	before, err := l.root.Lstat(recordName(id))
	if errors.Is(err, fs.ErrNotExist) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	if err := checkPrivate(before, l.owner, false); err != nil {
		return record{}, false, fmt.Errorf("record %s: %w", id, err)
	}
	f, err := l.root.OpenFile(recordName(id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return record{}, false, err
	}
	defer func() { _ = f.Close() }()
	if after, err := f.Stat(); err != nil || !os.SameFile(before, after) {
		return record{}, false, fmt.Errorf("record %s changed while it was opened", id)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err != nil {
		return record{}, false, err
	}
	var r record
	if err := decodeStrict(raw, maxRecord, &r); err != nil {
		return record{}, false, fmt.Errorf("record %s: %w", id, err)
	}
	if r.Version != Version || r.OperationID != id {
		return record{}, false, fmt.Errorf("record %s is corrupt", id)
	}
	return r, true, nil
}

// put replaces a record durably: a new file opened with O_EXCL, synced,
// renamed over the old one, and the directory synced.
func (l *Ledger) put(r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > maxRecord {
		return errors.New("record exceeds its limit")
	}
	tmp := "ops/.tmp-" + rand.Text()
	f, err := l.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = l.root.Remove(tmp) }()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := l.root.Rename(tmp, recordName(r.OperationID)); err != nil {
		return err
	}
	d, err := l.root.Open("ops")
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// begin records a new operation as running, or returns the record that
// already holds its ID. The ledger lock makes the check and the write one
// step, also across services.
func (l *Ledger) begin(r record) (record, bool, error) {
	// ".ledger" is not a service name, so the short ledger update never
	// waits for a service's operation.
	unlock, err := l.lock(".ledger", true)
	if err != nil {
		return record{}, false, err
	}
	defer unlock()
	if old, found, err := l.get(r.OperationID); err != nil || found {
		return old, found, err
	}
	return record{}, false, l.put(r)
}

// prune removes records of operations that finished before cutoff; at most
// limit per call, so that a request never waits long for it.
func (l *Ledger) prune(cutoff time.Time, limit int) {
	d, err := l.root.Open("ops")
	if err != nil {
		return
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return
	}
	for _, n := range names {
		if limit <= 0 {
			return
		}
		id, ok := strings.CutSuffix(n, ".json")
		if !ok || !uuidPattern.MatchString(id) {
			continue
		}
		fi, err := l.root.Lstat("ops/" + n)
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		r, found, err := l.get(id)
		if err != nil || !found || r.State == StateRunning || r.FinishedAt == 0 || !time.UnixMilli(r.FinishedAt).Before(cutoff) {
			continue
		}
		if l.root.Remove("ops/"+n) == nil {
			limit--
		}
	}
}

// realPath joins a host path below a test root.
func realPath(root, name string) string {
	if root == "" {
		return name
	}
	return filepath.Join(root, name)
}
