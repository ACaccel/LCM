package trigger

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxCalendarBytes bounds a calendar file; a decade of dates is ~4 KiB.
const maxCalendarBytes = 1 << 20

// calendar is a set of dates such as holidays or trading days.
type calendar struct {
	dates map[string]struct{}
}

func (c *calendar) contains(t time.Time) bool {
	_, ok := c.dates[t.Format(time.DateOnly)]
	return ok
}

// parseCalendar reads one date (YYYY-MM-DD) per line. Text after the date,
// blank lines and lines starting with # are ignored, so lines may carry a
// label such as "2026-01-01 New Year". Errors name the line, never its
// content, since the file is arbitrary data.
func parseCalendar(r io.Reader) (*calendar, error) {
	c := &calendar{dates: map[string]struct{}{}}
	data, err := io.ReadAll(io.LimitReader(r, maxCalendarBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCalendarBytes {
		return nil, fmt.Errorf("file is larger than %d bytes", maxCalendarBytes)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field, _, _ := strings.Cut(line, " ")
		field, _, _ = strings.Cut(field, "\t")
		d, err := time.Parse(time.DateOnly, field)
		if err != nil {
			return nil, fmt.Errorf("line %d: want a date as YYYY-MM-DD", n)
		}
		c.dates[d.Format(time.DateOnly)] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

// calendarFile loads a calendar and reloads it when the file changes, so
// that editing the holiday list needs no redeploy.
type calendarFile struct {
	path string
	mod  time.Time
	size int64
	cal  *calendar
}

// resolveCalendarPath maps a policy-supplied path to a file path. With a
// base directory only relative paths inside it are allowed; policies can
// then not point the loader at arbitrary files.
func resolveCalendarPath(base, p string) (string, error) {
	if base == "" {
		return filepath.Clean(p), nil
	}
	if filepath.IsAbs(p) || !filepath.IsLocal(p) {
		return "", fmt.Errorf("calendar %q: must be a relative path inside the calendar directory", p)
	}
	return filepath.Join(base, p), nil
}

// get returns the current calendar. When a reload fails, the last good
// calendar is kept and the error is returned alongside it; with no good
// calendar yet the error is returned alone.
func (f *calendarFile) get() (*calendar, error) {
	info, err := os.Stat(f.path)
	if err != nil {
		return f.cal, f.fail(err)
	}
	if f.cal != nil && info.ModTime().Equal(f.mod) && info.Size() == f.size {
		return f.cal, nil
	}
	file, err := os.Open(f.path)
	if err != nil {
		return f.cal, f.fail(err)
	}
	defer func() { _ = file.Close() }()
	cal, err := parseCalendar(file)
	if err != nil {
		return f.cal, f.fail(err)
	}
	f.cal, f.mod, f.size = cal, info.ModTime(), info.Size()
	return cal, nil
}

func (f *calendarFile) fail(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err // the message already names the path once
	}
	return fmt.Errorf("calendar %s: %w", f.path, err)
}
