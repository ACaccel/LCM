package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// hostStatusTTL bounds how often the root files are read: the pinned
// machine helper is a copy of the whole lcm binary, and the files change
// only when an administrator runs "lcm host apply".
const hostStatusTTL = 30 * time.Second

// digestPrefix is the length of the SHA-256 prefixes shown to clients,
// enough to tell two builds apart.
const digestPrefix = 12

// HostStatusFunc reads the state of the host components. It must only read.
type HostStatusFunc func() ([]hostsetup.ComponentStatus, error)

type hostFileDTO struct {
	Path      string `json:"path"`
	State     string `json:"state"`
	Installed string `json:"installed,omitempty"`
	Embedded  string `json:"embedded,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type hostComponentDTO struct {
	Name  string        `json:"name"`
	State string        `json:"state"`
	Files []hostFileDTO `json:"files"`
}

type hostComponentsDTO struct {
	Items     []hostComponentDTO `json:"items"`
	CheckedAt time.Time          `json:"checkedAt"`
}

// hostStatusCache serves one recent answer to every caller.
type hostStatusCache struct {
	mu   sync.Mutex
	at   time.Time
	body hostComponentsDTO
}

// handleHostComponents reports whether the root-owned host files match the
// ones embedded in the running binary. LCM only reads them as its own
// unprivileged account, so files it cannot read are reported as unknown;
// changing them stays with "sudo bin/lcm host apply" from a reviewed build.
func (s *Server) handleHostComponents(w http.ResponseWriter, r *http.Request) {
	if s.hostStatus == nil {
		writeJSON(w, http.StatusOK, hostComponentsDTO{Items: []hostComponentDTO{}, CheckedAt: s.now().UTC()})
		return
	}
	c := &s.hostCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if c.at.IsZero() || now.Sub(c.at) >= hostStatusTTL || now.Before(c.at) {
		list, err := s.hostStatus()
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		c.body, c.at = hostComponentsBody(list, now), now
	}
	writeJSON(w, http.StatusOK, c.body)
}

func hostComponentsBody(list []hostsetup.ComponentStatus, now time.Time) hostComponentsDTO {
	out := hostComponentsDTO{Items: make([]hostComponentDTO, 0, len(list)), CheckedAt: now.UTC()}
	for _, c := range list {
		d := hostComponentDTO{Name: c.Name, State: hostState(c.State), Files: make([]hostFileDTO, 0, len(c.Files))}
		for _, f := range c.Files {
			d.Files = append(d.Files, hostFileDTO{
				Path: f.Path, State: hostState(f.State),
				Installed: shorten(f.Installed), Embedded: shorten(f.Embedded), Detail: f.Detail,
			})
		}
		out.Items = append(out.Items, d)
	}
	return out
}

// hostState turns the display states of host setup ("up to date") into
// enum values.
func hostState(state string) string { return strings.ReplaceAll(state, " ", "_") }

func shorten(sum string) string {
	if len(sum) > digestPrefix {
		return sum[:digestPrefix]
	}
	return sum
}
