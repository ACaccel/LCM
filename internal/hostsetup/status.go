package hostsetup

import (
	"errors"
	"fmt"
	"io/fs"
)

// File and component states reported by Status.
const (
	StateUpToDate     = "up to date"
	StateOutdated     = "outdated"
	StateMissing      = "missing"
	StateNotInstalled = "not installed"
	StateUnknown      = "unknown"
)

// FileStatus compares one installed root file with the embedded version.
type FileStatus struct {
	Path  string `json:"path"`
	State string `json:"state"`
	// Installed and Embedded are hex SHA-256 digests; Installed is empty
	// when the file is missing or unreadable.
	Installed string `json:"installed,omitempty"`
	Embedded  string `json:"embedded"`
	Detail    string `json:"detail,omitempty"`
}

// ComponentStatus is the state of one host component.
type ComponentStatus struct {
	Name  string       `json:"name"`
	State string       `json:"state"`
	Files []FileStatus `json:"files"`
}

// Status compares the root files of every component with the versions
// this binary would install. It only reads, and works as any user: files
// the user cannot read are reported as unknown. The machine helper is
// compared with the running executable, the binary apply would pin.
func Status(h Host) ([]ComponentStatus, error) {
	exe, err := FileContent(h.FS, h.Executable)
	if err != nil {
		return nil, fmt.Errorf("read the running executable: %w", err)
	}
	var out []ComponentStatus
	for _, c := range []struct {
		name  string
		files []rootFile
	}{
		{ComponentCore, coreFiles},
		{ComponentMachinePower, powerFiles},
		{ComponentSecurityHost, securityFiles},
	} {
		cs := ComponentStatus{Name: c.name}
		for _, f := range c.files {
			want := exe
			if f.asset != "" {
				want = bytesContent(asset(f.asset))
			}
			cs.Files = append(cs.Files, fileStatus(h.FS, f, want))
		}
		cs.State = componentState(cs.Files)
		out = append(out, cs)
	}
	return out, nil
}

func fileStatus(fsys FS, f rootFile, want Content) FileStatus {
	st := FileStatus{Path: f.path, Embedded: want.Sum}
	info, err := fsys.Lstat(f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		st.State = StateMissing
		return st
	case err != nil:
		return unknown(st, err)
	case !info.IsRegular():
		st.State, st.Detail = StateOutdated, "not a regular file"
		return st
	}
	sum, err := hashFile(fsys, f.path)
	if err != nil {
		return unknown(st, err)
	}
	st.Installed = sum
	switch {
	case sum != want.Sum:
		st.State = StateOutdated
	case info.UID != 0 || info.GID != 0:
		st.State, st.Detail = StateOutdated, fmt.Sprintf("owned by %d:%d, not root", info.UID, info.GID)
	case info.Mode.Perm() != f.perm:
		st.State, st.Detail = StateOutdated, fmt.Sprintf("mode %04o, want %04o", info.Mode.Perm(), f.perm)
	default:
		st.State = StateUpToDate
	}
	return st
}

func unknown(st FileStatus, err error) FileStatus {
	st.State = StateUnknown
	if errors.Is(err, fs.ErrPermission) {
		st.Detail = "not readable as this user"
	} else {
		st.Detail = err.Error()
	}
	return st
}

// componentState summarizes the files: not installed when none exists,
// otherwise the most significant file state.
func componentState(files []FileStatus) string {
	count := map[string]int{}
	for _, f := range files {
		count[f.State]++
	}
	switch {
	case count[StateMissing] == len(files):
		return StateNotInstalled
	case count[StateOutdated] > 0:
		return StateOutdated
	case count[StateMissing] > 0:
		return StateMissing
	case count[StateUnknown] > 0:
		return StateUnknown
	}
	return StateUpToDate
}
