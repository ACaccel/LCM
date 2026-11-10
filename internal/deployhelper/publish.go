package deployhelper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// publishEntry is one file or directory of the content to publish, by its
// path relative to the published directory.
type publishEntry struct {
	rel  string
	info hostsetup.Info
}

// publishSource is what a release publishes.
type publishSource struct {
	tree  hostsetup.Tree
	dir   string
	dirs  []publishEntry
	files []publishEntry
	// names holds every published path, for the removal of the rest.
	names   map[string]bool
	skipped int
}

// publisher publishes one release's static content.
type publisher struct {
	h    *Helper
	src  *publishSource
	dst  string
	now  time.Time
	res  PublishResult
	keep []string
	days int
	// notes are problems of the removal, which does not fail the publish.
	notes []string
}

// publish copies the directory p.Dir of the release relDir to the
// service's publish directory: new and changed files first, the .html
// files last, then it removes what the release does not contain except
// the young files of retained directories. Only a failure before the
// removal is an error.
func (h *Helper) publish(svc, relDir string, p Publish) (*PublishResult, []string, error) {
	dst := hostsetup.ServicePublishDir(svc)
	if _, err := h.trustedPath(dst, false); err != nil {
		return nil, nil, fmt.Errorf("the publish directory cannot be used (%w); run sudo bin/lcm host allow or host apply", err)
	}
	src, err := h.readPublishSource(relDir, p.Dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = src.tree.Close() }()
	w := &publisher{h: h, src: src, dst: dst, now: h.Clock.Now(), keep: p.Retain, days: p.retainDays()}
	w.res.Skipped = src.skipped
	for _, d := range src.dirs {
		if err := w.ensureDir(d.rel); err != nil {
			return nil, nil, err
		}
	}
	// Pages last: until a page is replaced, the old one is served and
	// everything the new one names is already in place.
	for _, pages := range []bool{false, true} {
		for _, f := range src.files {
			if isPage(f.rel) != pages {
				continue
			}
			if err := w.file(f); err != nil {
				return nil, nil, err
			}
		}
	}
	w.prune(".")
	return &w.res, w.notes, nil
}

func isPage(rel string) bool { return strings.HasSuffix(strings.ToLower(rel), ".html") }

// readPublishSource walks the directory dir of the release relDir, which
// only root can change: every directory on the way and every entry must be
// root's, links are refused, entries starting with "." are skipped, and
// the walk is bounded like install.
func (h *Helper) readPublishSource(relDir, dir string) (*publishSource, error) {
	t, err := h.FS.OpenTree(relDir)
	if err != nil {
		return nil, fmt.Errorf("open the release: %w", err)
	}
	src := &publishSource{tree: t, dir: dir, names: map[string]bool{}}
	fail := func(err error) (*publishSource, error) {
		_ = t.Close()
		return nil, err
	}
	// The tree resolves links inside the release, so each component of dir
	// is looked at on its own.
	if dir != "." {
		parts := strings.Split(dir, "/")
		for i := range parts {
			at := path.Join(parts[:i+1]...)
			info, err := t.Lstat(at)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return fail(fmt.Errorf("publish: the release has no directory %s", dir))
			case err != nil:
				return fail(err)
			case info.IsLink():
				return fail(fmt.Errorf("publish: %s is a symbolic link; publish refuses links", at))
			case !info.IsDir():
				return fail(fmt.Errorf("publish: %s is not a directory", at))
			}
		}
	}
	var entries int
	var size int64
	var walk func(rel string) error
	walk = func(rel string) error {
		names, _, err := t.ReadDir(path.Join(dir, rel))
		if err != nil {
			return err
		}
		for _, n := range names {
			r := path.Join(rel, n)
			if strings.HasPrefix(n, ".") {
				src.skipped++
				continue
			}
			if entries++; entries > hostsetup.MaxTreeEntries {
				return fmt.Errorf("publish: more than %d entries", hostsetup.MaxTreeEntries)
			}
			info, err := t.Lstat(path.Join(dir, r))
			if err != nil {
				return err
			}
			switch {
			case info.IsLink():
				return fmt.Errorf("publish: %s is a symbolic link; publish refuses links", path.Join(dir, r))
			case info.UID != h.RootUID:
				return fmt.Errorf("publish: %s is not owned by root", path.Join(dir, r))
			case info.IsDir():
				src.dirs = append(src.dirs, publishEntry{rel: r, info: info})
				src.names[r] = true
				if err := walk(r); err != nil {
					return err
				}
			case info.IsRegular():
				if size += info.Size; size > hostsetup.MaxTreeBytes {
					return fmt.Errorf("publish: more than %d bytes", int64(hostsetup.MaxTreeBytes))
				}
				src.files = append(src.files, publishEntry{rel: r, info: info})
				src.names[r] = true
			default:
				return fmt.Errorf("publish: %s is not a regular file or directory", path.Join(dir, r))
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return fail(err)
	}
	return src, nil
}

// ensureDir makes the directory rel of the publish directory, replacing a
// file or link in its place.
func (w *publisher) ensureDir(rel string) error {
	fsys, p := w.h.FS, path.Join(w.dst, rel)
	info, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case info.IsDir() && info.UID != w.h.RootUID:
		return fmt.Errorf("publish: %s is not owned by root", p)
	case info.IsDir() && info.Mode.Perm() != 0o755:
		return fsys.Chmod(p, info, 0o755)
	case info.IsDir():
		return nil
	default:
		if err := fsys.Remove(p); err != nil {
			return err
		}
	}
	return fsys.Mkdir(p, 0o755, w.h.RootUID, w.h.RootGID)
}

// file publishes one file: a new or changed one is written through a
// temporary file and a rename, an unchanged one only gets the time of the
// publish.
func (w *publisher) file(f publishEntry) error {
	fsys, p := w.h.FS, path.Join(w.dst, f.rel)
	perm := releasePerm(f.info)
	have, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.res.Written++
	case err != nil:
		return err
	case have.IsRegular() && have.UID == w.h.RootUID && have.Mode.Perm() == perm && have.Size == f.info.Size:
		same, err := w.same(f, p)
		if err != nil {
			return err
		}
		if same {
			w.res.Unchanged++
			return fsys.Chtimes(p, have, w.now)
		}
		w.res.Replaced++
	case have.IsDir():
		if err := fsys.RemoveAll(p); err != nil {
			return err
		}
		w.res.Replaced++
	default:
		w.res.Replaced++
	}
	in, info, err := w.src.tree.Open(path.Join(w.src.dir, f.rel))
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if info.Dev != f.info.Dev || info.Ino != f.info.Ino {
		return fmt.Errorf("publish: %s changed while it was published", f.rel)
	}
	if err := fsys.WriteFile(p, io.LimitReader(in, info.Size), perm, w.h.RootUID, w.h.RootGID); err != nil {
		return fmt.Errorf("publish %s: %w", f.rel, err)
	}
	made, err := fsys.Lstat(p)
	if err != nil {
		return err
	}
	return fsys.Chtimes(p, made, w.now)
}

// same compares a published file with the release's.
func (w *publisher) same(f publishEntry, p string) (bool, error) {
	a, _, err := w.src.tree.Open(path.Join(w.src.dir, f.rel))
	if err != nil {
		return false, err
	}
	defer func() { _ = a.Close() }()
	b, err := w.h.FS.Open(p)
	if err != nil {
		return false, err
	}
	defer func() { _ = b.Close() }()
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		n, errA := io.ReadFull(a, bufA)
		m, errB := io.ReadFull(b, bufB)
		if n != m || !bytes.Equal(bufA[:n], bufB[:m]) {
			return false, nil
		}
		doneA := errors.Is(errA, io.EOF) || errors.Is(errA, io.ErrUnexpectedEOF)
		doneB := errors.Is(errB, io.EOF) || errors.Is(errB, io.ErrUnexpectedEOF)
		switch {
		case errA != nil && !doneA:
			return false, errA
		case errB != nil && !doneB:
			return false, errB
		case doneA || doneB:
			return doneA && doneB, nil
		}
	}
}

// retained reports whether rel lies below a retain directory. Names
// starting with "." are never published, so they are never retained.
func (w *publisher) retained(rel string) bool {
	for _, c := range strings.Split(rel, "/") {
		if strings.HasPrefix(c, ".") {
			return false
		}
	}
	for _, k := range w.keep {
		if strings.HasPrefix(rel, k) {
			return true
		}
	}
	return false
}

// prune removes below the directory rel of the publish directory what the
// release does not contain, except young files of retained directories,
// and directories left empty. It reports whether rel is empty afterwards.
func (w *publisher) prune(rel string) bool {
	fsys, dir := w.h.FS, path.Join(w.dst, rel)
	names, err := fsys.ReadDir(dir)
	if err != nil {
		w.note(dir, err)
		return false
	}
	left := 0
	for _, n := range names {
		r := path.Join(rel, n)
		p := path.Join(w.dst, r)
		info, err := fsys.Lstat(p)
		if err != nil {
			w.note(p, err)
			left++
			continue
		}
		if info.IsDir() {
			if w.prune(r) && !w.src.names[r] {
				if err := fsys.Remove(p); err != nil {
					w.note(p, err)
					left++
				}
				continue
			}
			left++
			continue
		}
		if w.src.names[r] {
			left++
			continue
		}
		if info.IsRegular() && w.retained(r) && w.now.Sub(info.ModTime) < time.Duration(w.days)*24*time.Hour {
			w.res.Retained++
			left++
			continue
		}
		if err := fsys.Remove(p); err != nil {
			w.note(p, err)
			left++
			continue
		}
		w.res.Deleted++
	}
	return left == 0
}

func (w *publisher) note(p string, err error) {
	if len(w.notes) < 20 {
		w.notes = append(w.notes, fmt.Sprintf("publish: %s was not removed: %v", p, err))
	}
}
