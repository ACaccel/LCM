package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/model"
)

// maxApplyBytes bounds the body of POST /apply, which carries many
// documents.
const maxApplyBytes = 16 << 20

// managedByDTO says which config source manages a resource.
type managedByDTO struct {
	Source  string `json:"source"`
	Path    string `json:"path"`
	Commit  string `json:"commit"`
	Drifted bool   `json:"drifted"`
	// Conflict: a commit was skipped because of local edits.
	Conflict bool `json:"conflict"`
}

func toManagedBy(o configsync.Ownership) *managedByDTO {
	return &managedByDTO{Source: o.Source, Path: o.Path, Commit: o.Commit, Drifted: o.Drifted, Conflict: o.Conflict}
}

// managedIndex maps every managed resource to its managedBy for list views.
// A failure is logged and answered as "no information" because the lists
// are useful without it.
func (s *Server) managedIndex(r *http.Request, kind model.Kind) map[string]*managedByDTO {
	if s.sync == nil {
		return nil
	}
	owned, err := s.sync.Ownership(r.Context(), "")
	if err != nil {
		s.log.Error("cannot read config ownership", "error", err, "request_id", requestID(r.Context()))
		return nil
	}
	out := map[string]*managedByDTO{}
	for _, o := range owned {
		if o.Kind == kind {
			out[o.Name] = toManagedBy(o)
		}
	}
	return out
}

// managedFor returns managedBy of one resource, nil when it is not managed.
func (s *Server) managedFor(r *http.Request, kind model.Kind, name string) *managedByDTO {
	if s.sync == nil {
		return nil
	}
	o, err := s.sync.Owner(r.Context(), kind, name)
	if err != nil {
		s.log.Error("cannot read config ownership", "error", err, "resource", string(kind)+"/"+name, "request_id", requestID(r.Context()))
		return nil
	}
	if o == nil {
		return nil
	}
	return toManagedBy(*o)
}

// requireSync answers 503 when the server runs without config sync.
func (s *Server) requireSync(w http.ResponseWriter) bool {
	if s.sync == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "config sync is not available", nil)
		return false
	}
	return true
}

type resultDTO struct {
	Index    int         `json:"index"`
	Kind     string      `json:"kind"`
	Name     string      `json:"name"`
	Action   string      `json:"action"`
	Version  int         `json:"version"`
	Diff     string      `json:"diff"`
	Reason   string      `json:"reason"`
	Issues   []issueBody `json:"issues"`
	Warnings []issueBody `json:"warnings"`
	Conflict bool        `json:"conflict"`
	Drifted  bool        `json:"drifted"`
	// Path is the file of a document that came from a config source.
	Path string `json:"path"`
}

func toResultDTO(r configsync.Result) resultDTO {
	return resultDTO{
		Index: r.Index, Kind: string(r.Kind), Name: r.Name, Action: string(r.Action), Version: r.Version, Diff: r.Diff,
		Reason: r.Reason, Issues: issuesBody(r.Issues), Warnings: issuesBody(r.Warnings), Conflict: r.Conflict,
		Drifted: r.Drifted, Path: r.Path,
	}
}

func toResultDTOs(rs []configsync.Result) []resultDTO {
	out := make([]resultDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, toResultDTO(r))
	}
	return out
}

// readStream reads a multi-document request body. It has written the
// response on failure.
func readStream(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid Content-Type", nil)
			return nil, false
		case mt != "application/yaml" && mt != "application/x-yaml" && mt != "text/yaml":
			writeError(w, http.StatusBadRequest, codeBadRequest, "unsupported Content-Type",
				map[string]any{"contentType": mt, "supported": []string{"application/yaml"}})
			return nil, false
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxApplyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "request body too large",
				map[string]any{"limitBytes": maxApplyBytes})
			return nil, false
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "cannot read request body", nil)
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "request body is empty", nil)
		return nil, false
	}
	return body, true
}

// handleApply applies a multi-document YAML body. Problems in single
// documents are results, not errors; only a body without any usable
// document is a 422.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	dryRun, ok := boolParam(w, r, "dryRun")
	if !ok {
		return
	}
	force, ok := boolParam(w, r, "force")
	if !ok {
		return
	}
	body, ok := readStream(w, r)
	if !ok {
		return
	}
	docs := configsync.SplitDocuments("-", body)
	if err := configsync.StreamError(docs); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	results, err := s.sync.Apply(r.Context(), docs, configsync.Options{DryRun: dryRun, Force: force})
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dryRun": dryRun, "items": toResultDTOs(results)})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	q := r.URL.Query()
	sel := configsync.Selector{Name: q.Get("name")}
	if raw := q.Get("kind"); raw != "" {
		kind, err := configsync.ParseKind(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error(), map[string]any{"param": "kind"})
			return
		}
		sel.Kind = kind
	}
	if sel.Name != "" && sel.Kind == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "name needs kind", map[string]any{"param": "name"})
		return
	}
	out, err := s.sync.Export(r.Context(), sel)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(out)
}

type sourceDTO struct {
	Name       string     `json:"name"`
	Repo       string     `json:"repo"`
	Branch     string     `json:"branch"`
	Path       string     `json:"path"`
	Interval   string     `json:"interval"`
	Enabled    bool       `json:"enabled"`
	LastCommit string     `json:"lastCommit"`
	LastSyncAt *time.Time `json:"lastSyncAt"`
	LastError  string     `json:"lastError"`
	Syncing    bool       `json:"syncing"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func toSourceDTO(src configsync.Source) sourceDTO {
	return sourceDTO{
		Name: src.Name, Repo: src.Repo, Branch: src.Branch, Path: src.Path, Interval: src.Interval.String(),
		Enabled: src.Enabled, LastCommit: src.LastCommit, LastSyncAt: optTime(src.LastSyncAt), LastError: src.LastError,
		Syncing: src.Syncing, CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt,
	}
}

// sourceRequest is the body of source create and replace.
type sourceRequest struct {
	Name     string `json:"name"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Path     string `json:"path"`
	Interval string `json:"interval"`
	Enabled  *bool  `json:"enabled"`
}

// source converts the request; enabled defaults to true.
func (req sourceRequest) source(w http.ResponseWriter) (configsync.Source, bool) {
	src := configsync.Source{Name: req.Name, Repo: req.Repo, Branch: req.Branch, Path: req.Path, Enabled: req.Enabled == nil || *req.Enabled}
	if req.Interval != "" {
		d, err := time.ParseDuration(req.Interval)
		if err != nil {
			writeFieldError(w, "interval", "must be a duration such as \"1m\"")
			return src, false
		}
		src.Interval = d
	}
	return src, true
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	srcs, err := s.sync.ListSources(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, srcs)
	if !ok {
		return
	}
	items := make([]sourceDTO, 0, len(page))
	for _, src := range page {
		items = append(items, toSourceDTO(src))
	}
	writeList(w, items, next)
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	var req sourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	src, ok := req.source(w)
	if !ok {
		return
	}
	created, err := s.sync.CreateSource(r.Context(), src)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Location", "/api/v1/config/sources/"+created.Name)
	writeJSON(w, http.StatusCreated, toSourceDTO(created))
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	src, err := s.sync.GetSource(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, toSourceDTO(src))
}

func (s *Server) handlePutSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	name := r.PathValue("name")
	var req sourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name != "" && req.Name != name {
		writeFieldError(w, "name", "must equal the source name in the URL \""+name+"\"")
		return
	}
	req.Name = name
	src, ok := req.source(w)
	if !ok {
		return
	}
	updated, err := s.sync.UpdateSource(r.Context(), src)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, toSourceDTO(updated))
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	if err := s.sync.DeleteSource(r.Context(), r.PathValue("name")); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncSource starts a sync of the source in the background and
// answers 202 with the source. Clients read lastSyncAt, lastCommit and
// lastError from it once `syncing` is false. Requests while a sync runs
// join it.
func (s *Server) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	name := r.PathValue("name")
	if _, err := s.sync.RequestSync(r.Context(), name); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	src, err := s.sync.GetSource(r.Context(), name)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Location", "/api/v1/config/sources/"+name)
	writeJSON(w, http.StatusAccepted, toSourceDTO(src))
}

type ownershipDTO struct {
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Source         string    `json:"source"`
	Path           string    `json:"path"`
	Commit         string    `json:"commit"`
	AppliedVersion int       `json:"appliedVersion"`
	AppliedAt      time.Time `json:"appliedAt"`
	Drifted        bool      `json:"drifted"`
	Missing        bool      `json:"missing"`
	Conflict       bool      `json:"conflict"`
	ConflictCommit string    `json:"conflictCommit"`
	Acknowledged   bool      `json:"acknowledged"`
	Orphaned       bool      `json:"orphaned"`
}

func toOwnershipDTO(o configsync.Ownership) ownershipDTO {
	return ownershipDTO{
		Kind: string(o.Kind), Name: o.Name, Source: o.Source, Path: o.Path, Commit: o.Commit,
		AppliedVersion: o.AppliedVersion, AppliedAt: o.AppliedAt, Drifted: o.Drifted, Missing: o.Missing,
		Conflict: o.Conflict, ConflictCommit: o.ConflictCommit, Acknowledged: o.Acknowledged, Orphaned: o.Orphaned,
	}
}

func (s *Server) handleListOwnership(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var kind model.Kind
	if raw := q.Get("kind"); raw != "" {
		var err error
		if kind, err = configsync.ParseKind(raw); err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, err.Error(), map[string]any{"param": "kind"})
			return
		}
	}
	owned, err := s.sync.Ownership(r.Context(), q.Get("source"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	filtered := owned[:0:0]
	for _, o := range owned {
		if kind == "" || o.Kind == kind {
			filtered = append(filtered, o)
		}
	}
	page, next, ok := pageSlice(w, p, filtered)
	if !ok {
		return
	}
	items := make([]ownershipDTO, 0, len(page))
	for _, o := range page {
		items = append(items, toOwnershipDTO(o))
	}
	writeList(w, items, next)
}

// ownershipTarget reads the kind and name of the path. An unknown kind names
// nothing, so it is a 404.
func ownershipTarget(w http.ResponseWriter, r *http.Request) (model.Kind, string, bool) {
	kind, err := configsync.ParseKind(r.PathValue("kind"))
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown resource kind", nil)
		return "", "", false
	}
	return kind, r.PathValue("name"), true
}

func (s *Server) handleResolveOwnership(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	kind, name, ok := ownershipTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		Use string `json:"use"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Use != "git" && req.Use != "current" {
		writeFieldError(w, "use", "must be git or current")
		return
	}
	o, err := s.sync.Resolve(r.Context(), kind, name, req.Use == "git")
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, toOwnershipDTO(o))
}

func (s *Server) handleReleaseOwnership(w http.ResponseWriter, r *http.Request) {
	if !s.requireSync(w) {
		return
	}
	kind, name, ok := ownershipTarget(w, r)
	if !ok {
		return
	}
	if err := s.sync.Release(r.Context(), kind, name); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
