package api

import (
	_ "embed"
	"net/http"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

//go:embed openapi.yaml
var openAPIDocument []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(openAPIDocument)
}

func (s *Server) handleNodeTypes(w http.ResponseWriter, _ *http.Request) {
	schemas := s.registry.Schemas()
	// Clients iterate these lists, so they are never null.
	for i := range schemas {
		if schemas[i].Ports == nil {
			schemas[i].Ports = []string{}
		}
		if schemas[i].Props == nil {
			schemas[i].Props = []node.Prop{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": schemas})
}

type validateDTO struct {
	Kind     string      `json:"kind"`
	Name     string      `json:"name"`
	Valid    bool        `json:"valid"`
	Issues   []issueBody `json:"issues"`
	Warnings []issueBody `json:"warnings"`
}

// handleValidate checks one document of any kind without side effects. A
// document with problems is a normal answer here, so it is a 200 with
// valid=false rather than a 422.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, ok := readDocument(w, r)
	if !ok {
		return
	}
	out := validateDTO{Issues: []issueBody{}, Warnings: []issueBody{}}
	finish := func() {
		out.Valid = len(out.Issues) == 0
		writeJSON(w, http.StatusOK, out)
	}

	doc, err := model.Decode(body)
	if err != nil {
		var is model.Issues
		if !asIssues(err, &is) {
			s.fail(w, r, err, nil)
			return
		}
		out.Issues = issuesBody(is)
		out.Kind, out.Name = documentHead(body)
		finish()
		return
	}
	out.Kind, out.Name = string(doc.DocKind()), doc.DocName()

	var is model.Issues
	switch d := doc.(type) {
	case *model.Policy:
		warnings, err := s.engine.Validate(r.Context(), body)
		if err != nil {
			if !asIssues(err, &is) {
				s.fail(w, r, err, nil)
				return
			}
			out.Issues = issuesBody(is)
		} else {
			out.Warnings = issuesBody(warnings)
		}
	case *model.Service:
		out.Issues = issuesBody(s.serviceIssues(r, d))
	}
	finish()
}

// documentHead reads the kind and name of a document that failed to
// decode, so the caller can tell which document the issues belong to.
func documentHead(body []byte) (kind, name string) {
	var head struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(body, &head); err != nil {
		return "", ""
	}
	return head.Kind, head.Metadata.Name
}

type lockDTO struct {
	Name       string    `json:"name"`
	Owner      string    `json:"owner"`
	AcquiredAt time.Time `json:"acquiredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (s *Server) handleListLocks(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	locks, err := s.locks.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, locks)
	if !ok {
		return
	}
	items := make([]lockDTO, 0, len(page))
	for _, l := range page {
		items = append(items, lockDTO{Name: l.Name, Owner: l.Owner, AcquiredAt: l.AcquiredAt, ExpiresAt: l.ExpiresAt})
	}
	writeList(w, items, next)
}

// handleForceReleaseLock frees a lock regardless of its holder. The lock
// manager audits the release.
func (s *Server) handleForceReleaseLock(w http.ResponseWriter, r *http.Request) {
	if err := s.locks.ForceRelease(r.Context(), r.PathValue("name")); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
