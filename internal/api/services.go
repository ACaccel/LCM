package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
)

// docMeta is the version metadata every document response carries.
type docMeta struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy actorDTO  `json:"updatedBy"`
}

type versionDTO struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy actorDTO  `json:"createdBy"`
	// RestoredFrom is the version this one was restored from, or null.
	RestoredFrom *int `json:"restoredFrom"`
}

type hostDTO struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.catalog.Hosts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]hostDTO, 0, len(hosts))
	for _, h := range hosts {
		items = append(items, hostDTO{Name: h.Name, Kind: h.Kind, Address: h.Address})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type serviceSummaryDTO struct {
	Name       string            `json:"name"`
	Host       string            `json:"host"`
	Labels     map[string]string `json:"labels"`
	State      string            `json:"state"`
	StateSince time.Time         `json:"stateSince"`
	Health     string            `json:"health"`
	// Version is the configuration document version.
	Version      int       `json:"version"`
	ConfigSource string    `json:"configSource"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// DeployedVersion is the commit of the last successful deployment, or
	// empty.
	DeployedVersion string `json:"deployedVersion"`
}

func (s *Server) healthOf(name string) (monitor.ServiceStatus, bool) {
	return s.monitor.Service(name)
}

func labelsOf(rec *catalog.Record) map[string]string {
	if rec.Spec.Metadata.Labels == nil {
		return map[string]string{}
	}
	return rec.Spec.Metadata.Labels
}

func configSourceOf(rec *catalog.Record) string {
	if rec.Spec.Spec.ConfigSource == "" {
		return string(model.ConfigSourceGUI)
	}
	return string(rec.Spec.Spec.ConfigSource)
}

// deployedVersion returns the commit of the newest successful deployment.
func deployedVersion(deps []catalog.Deployment) string {
	for _, d := range deps {
		if d.Outcome == catalog.OutcomeSucceeded && d.Version != "" {
			return d.Version
		}
	}
	return ""
}

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	wantLabels := map[string]string{}
	for _, raw := range q["label"] {
		k, v, found := strings.Cut(raw, "=")
		if !found || k == "" {
			writeError(w, http.StatusBadRequest, codeBadRequest, "label filter must be key=value",
				map[string]any{"param": "label"})
			return
		}
		wantLabels[k] = v
	}
	wantState := q.Get("state")
	if wantState != "" && !catalog.State(wantState).Valid() {
		writeError(w, http.StatusBadRequest, codeBadRequest, "unknown state", map[string]any{"param": "state"})
		return
	}
	wantHealth := q.Get("health")
	switch monitor.Health(wantHealth) {
	case "", monitor.HealthUnknown, monitor.HealthHealthy, monitor.HealthDegraded, monitor.HealthDown:
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "unknown health", map[string]any{"param": "health"})
		return
	}

	recs, err := s.catalog.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	health := map[string]monitor.Health{}
	for _, st := range s.monitor.Services() {
		health[st.Service] = st.Health
	}
	healthOf := func(name string) monitor.Health {
		if h, ok := health[name]; ok {
			return h
		}
		return monitor.HealthUnknown
	}
	matched := recs[:0:0]
	for _, rec := range recs {
		if wantState != "" && string(rec.State) != wantState {
			continue
		}
		if wantHealth != "" && string(healthOf(rec.Name)) != wantHealth {
			continue
		}
		labels := rec.Spec.Metadata.Labels
		okLabels := true
		for k, v := range wantLabels {
			if got, has := labels[k]; !has || got != v {
				okLabels = false
				break
			}
		}
		if okLabels {
			matched = append(matched, rec)
		}
	}
	page, next, ok := pageSlice(w, p, matched)
	if !ok {
		return
	}
	items := make([]serviceSummaryDTO, 0, len(page))
	for i := range page {
		rec := &page[i]
		deps, err := s.catalog.Deployments(r.Context(), rec.Name, 20, 0)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = append(items, serviceSummaryDTO{
			Name: rec.Name, Host: rec.Host, Labels: labelsOf(rec), State: string(rec.State), StateSince: rec.StateSince,
			Health: string(healthOf(rec.Name)), Version: rec.Version, ConfigSource: configSourceOf(rec),
			UpdatedAt: rec.UpdatedAt, DeployedVersion: deployedVersion(deps),
		})
	}
	writeList(w, items, next)
}

type serviceDocDTO struct {
	Document *model.Service `json:"document"`
	docMeta
}

func (s *Server) serviceDoc(r *http.Request, name string, version int) (serviceDocDTO, error) {
	svc, info, err := s.catalog.GetVersion(r.Context(), name, version)
	if err != nil {
		return serviceDocDTO{}, err
	}
	return serviceDocDTO{
		Document: svc,
		docMeta:  docMeta{Version: version, UpdatedAt: info.CreatedAt, UpdatedBy: toActorDTO(info.CreatedBy)},
	}, nil
}

type checkStatusDTO struct {
	Name                string     `json:"name"`
	Type                string     `json:"type"`
	Critical            bool       `json:"critical"`
	IntervalMs          int64      `json:"intervalMs"`
	Status              string     `json:"status"`
	Stale               bool       `json:"stale"`
	Since               *time.Time `json:"since"`
	Message             string     `json:"message"`
	LastResult          *time.Time `json:"lastResult"`
	LastLatencyMs       int64      `json:"lastLatencyMs"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func toCheckStatusDTO(c monitor.CheckStatus) checkStatusDTO {
	return checkStatusDTO{
		Name: c.Name, Type: c.Type, Critical: c.Critical, IntervalMs: c.Interval.Milliseconds(), Status: string(c.Status),
		Stale: c.Stale, Since: optTime(c.Since), Message: c.Message, LastResult: optTime(c.LastResult),
		LastLatencyMs: c.LastLatency.Milliseconds(), ConsecutiveFailures: c.ConsecutiveFailures,
	}
}

type deploymentDTO struct {
	ID              int64      `json:"id"`
	Service         string     `json:"service"`
	Action          string     `json:"action"`
	Version         string     `json:"version"`
	PreviousVersion string     `json:"previousVersion"`
	StartedAt       time.Time  `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	// Outcome is running, succeeded, failed or interrupted.
	Outcome    string   `json:"outcome"`
	Actor      actorDTO `json:"actor"`
	RunID      string   `json:"runId"`
	Error      string   `json:"error"`
	LogExcerpt string   `json:"logExcerpt"`
}

func toDeploymentDTO(d catalog.Deployment) deploymentDTO {
	return deploymentDTO{
		ID: d.ID, Service: d.Service, Action: d.Action, Version: d.Version, PreviousVersion: d.PreviousVersion,
		StartedAt: d.StartedAt, FinishedAt: optTime(d.FinishedAt), Outcome: d.Outcome, Actor: toActorDTO(d.Actor),
		RunID: d.RunID, Error: d.Error, LogExcerpt: d.LogExcerpt,
	}
}

type serviceDetailDTO struct {
	serviceDocDTO
	Name            string           `json:"name"`
	Host            string           `json:"host"`
	State           string           `json:"state"`
	StateSince      time.Time        `json:"stateSince"`
	CreatedAt       time.Time        `json:"createdAt"`
	Health          string           `json:"health"`
	HealthSince     *time.Time       `json:"healthSince"`
	Checks          []checkStatusDTO `json:"checks"`
	Alerts          []monitor.Alert  `json:"alerts"`
	DeployedVersion string           `json:"deployedVersion"`
	LastDeployment  *deploymentDTO   `json:"lastDeployment"`
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("svc")
	rec, err := s.catalog.Get(r.Context(), name)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	doc, err := s.serviceDoc(r, name, rec.Version)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	alerts, err := s.monitor.FiringAlerts(r.Context(), name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	deps, err := s.catalog.Deployments(r.Context(), name, 20, 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := serviceDetailDTO{
		serviceDocDTO: doc, Name: rec.Name, Host: rec.Host, State: string(rec.State), StateSince: rec.StateSince,
		CreatedAt: rec.CreatedAt, Health: string(monitor.HealthUnknown), Checks: []checkStatusDTO{}, Alerts: alerts,
		DeployedVersion: deployedVersion(deps),
	}
	if out.Alerts == nil {
		out.Alerts = []monitor.Alert{}
	}
	if st, ok := s.healthOf(name); ok {
		out.Health, out.HealthSince = string(st.Health), optTime(st.Since)
		for _, c := range st.Checks {
			out.Checks = append(out.Checks, toCheckStatusDTO(c))
		}
	}
	if len(deps) > 0 {
		d := toDeploymentDTO(deps[0])
		out.LastDeployment = &d
	}
	w.Header().Set("ETag", etag(rec.Version))
	writeJSON(w, http.StatusOK, out)
}

// decodeServiceBody reads and decodes a service document. It has written
// the response on failure.
func (s *Server) decodeServiceBody(w http.ResponseWriter, r *http.Request) ([]byte, *model.Service, bool) {
	body, ok := readDocument(w, r)
	if !ok {
		return nil, nil, false
	}
	svc, err := model.DecodeService(body)
	if err != nil {
		s.fail(w, r, err, nil)
		return nil, nil, false
	}
	return body, svc, true
}

// serviceIssues returns the problems of a decoded service that only the
// running system can tell: unknown host and invalid check props.
func (s *Server) serviceIssues(r *http.Request, svc *model.Service) model.Issues {
	var out model.Issues
	if hosts, err := s.catalog.Hosts(r.Context()); err == nil {
		known := slices.ContainsFunc(hosts, func(h catalog.Host) bool { return h.Name == svc.Spec.EffectiveHost() })
		if !known {
			out = append(out, model.Issue{Path: "spec.host", Message: "unknown host \"" + svc.Spec.EffectiveHost() + "\""})
		}
	}
	return append(out, s.monitor.ValidateChecks(svc.Spec.Checks, "spec.checks")...)
}

func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	body, svc, ok := s.decodeServiceBody(w, r)
	if !ok {
		return
	}
	if is := s.monitor.ValidateChecks(svc.Spec.Checks, "spec.checks"); len(is) > 0 {
		writeIssues(w, is)
		return
	}
	res, err := s.catalog.Put(r.Context(), body, 0)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	doc, err := s.serviceDoc(r, svc.Metadata.Name, res.Version)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Location", "/api/v1/services/"+svc.Metadata.Name)
	w.Header().Set("ETag", etag(res.Version))
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) handlePutService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("svc")
	im, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	if !im.present || im.wildcard {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"If-Match with the current version is required; create services with POST /services", nil)
		return
	}
	body, svc, ok := s.decodeServiceBody(w, r)
	if !ok {
		return
	}
	if svc.Metadata.Name != name {
		writeFieldError(w, "metadata.name", "must equal the service name in the URL \""+name+"\"")
		return
	}
	if is := s.monitor.ValidateChecks(svc.Spec.Checks, "spec.checks"); len(is) > 0 {
		writeIssues(w, is)
		return
	}
	res, err := s.catalog.Put(r.Context(), body, im.version)
	if err != nil {
		var extra map[string]any
		if errors.Is(err, catalog.ErrConflict) {
			if cur, gerr := s.catalog.Get(r.Context(), name); gerr == nil {
				extra = map[string]any{"currentVersion": cur.Version, "expectedVersion": im.version}
			}
		}
		s.fail(w, r, err, extra)
		return
	}
	doc, err := s.serviceDoc(r, name, res.Version)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(res.Version))
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	if err := s.catalog.Delete(r.Context(), r.PathValue("svc")); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toVersionDTO(v catalog.VersionInfo) versionDTO {
	d := versionDTO{Version: v.Version, CreatedAt: v.CreatedAt, CreatedBy: toActorDTO(v.CreatedBy)}
	if v.RestoredFrom > 0 {
		d.RestoredFrom = &v.RestoredFrom
	}
	return d
}

func (s *Server) handleServiceVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	vs, err := s.catalog.Versions(r.Context(), r.PathValue("svc"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, vs)
	if !ok {
		return
	}
	items := make([]versionDTO, 0, len(page))
	for _, v := range page {
		items = append(items, toVersionDTO(v))
	}
	writeList(w, items, next)
}

type serviceVersionDTO struct {
	serviceDocDTO
	RestoredFrom *int `json:"restoredFrom"`
}

func (s *Server) handleGetServiceVersion(w http.ResponseWriter, r *http.Request) {
	v, ok := pathVersion(w, r, "v")
	if !ok {
		return
	}
	svc, info, err := s.catalog.GetVersion(r.Context(), r.PathValue("svc"), v)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := serviceVersionDTO{serviceDocDTO: serviceDocDTO{
		Document: svc, docMeta: docMeta{Version: v, UpdatedAt: info.CreatedAt, UpdatedBy: toActorDTO(info.CreatedBy)},
	}}
	if info.RestoredFrom > 0 {
		out.RestoredFrom = &info.RestoredFrom
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRestoreService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("svc")
	var req struct {
		Version int `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Version <= 0 {
		writeFieldError(w, "version", "must be a positive version number")
		return
	}
	im, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	expected := im.version
	if !im.present || im.wildcard {
		cur, err := s.catalog.Get(r.Context(), name)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		expected = cur.Version
	}
	res, err := s.catalog.Restore(r.Context(), name, req.Version, expected)
	if err != nil {
		var extra map[string]any
		if errors.Is(err, catalog.ErrConflict) {
			if cur, gerr := s.catalog.Get(r.Context(), name); gerr == nil {
				extra = map[string]any{"currentVersion": cur.Version, "expectedVersion": expected}
			}
		}
		s.fail(w, r, err, extra)
		return
	}
	doc, err := s.serviceDoc(r, name, res.Version)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(res.Version))
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	before, ok := idCursor(w, p)
	if !ok {
		return
	}
	deps, err := s.catalog.Deployments(r.Context(), r.PathValue("svc"), p.limit+1, before)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var next *string
	if len(deps) > p.limit {
		deps = deps[:p.limit]
		next = encodeCursor(int64String(deps[p.limit-1].ID))
	}
	items := make([]deploymentDTO, 0, len(deps))
	for _, d := range deps {
		items = append(items, toDeploymentDTO(d))
	}
	writeList(w, items, next)
}

func (s *Server) listTimeline(w http.ResponseWriter, r *http.Request, service string) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	before, ok := idCursor(w, p)
	if !ok {
		return
	}
	from, ok := timeParam(w, r, "from")
	if !ok {
		return
	}
	to, ok := timeParam(w, r, "to")
	if !ok {
		return
	}
	var kinds []string
	for _, raw := range r.URL.Query()["kinds"] {
		for k := range strings.SplitSeq(raw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				kinds = append(kinds, k)
			}
		}
	}
	page, err := s.catalog.Timeline(r.Context(), catalog.TimelineQuery{
		Service: service, From: from, To: to, Kinds: kinds, Limit: p.limit, Before: before,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]events.TimelineEntry, 0, len(page.Entries))
	for _, e := range page.Entries {
		items = append(items, events.NewTimelineEntry(e))
	}
	var next *string
	if page.Next > 0 {
		next = encodeCursor(int64String(page.Next))
	}
	writeList(w, items, next)
}

func (s *Server) handleServiceTimeline(w http.ResponseWriter, r *http.Request) {
	s.listTimeline(w, r, r.PathValue("svc"))
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	s.listTimeline(w, r, "")
}

// actorOf returns the caller, which the middleware guarantees.
func actorOf(r *http.Request) auth.Actor {
	a, _ := auth.ActorFrom(r.Context())
	return a
}
