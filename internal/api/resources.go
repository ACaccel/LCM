package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

// errUnsupported answers a route used on a resource type that does not
// offer it, such as observing a service; fail maps it to 409 unsupported.
var errUnsupported = errors.New("api: the resource type does not support this")

func unsupported(rec resource.Record, what string) error {
	return fmt.Errorf("%w: %s resources have no %s", errUnsupported, rec.Type, what)
}

// ResourceType connects one resource type to the API. Type is required; the
// optional parts are nil for types that do not have them.
type ResourceType struct {
	Type resource.Type
	// Observations serves observe, history and events (machine, router).
	Observations Observations
	// Operations serves capabilities and durable operations (machine,
	// router).
	Operations Operations
}

// ActionCatalog is implemented by resource types that can list the actions
// they offer without a concrete resource, for GET /resource-types. A type
// without it is described with an empty action list.
type ActionCatalog interface {
	ActionCatalog() []resource.Action
}

// ObservedItem is one stored snapshot or event. ID orders and pages the
// items, together with At (Unix milliseconds of the time the event occurred)
// for events read newest first; Body is what the client receives.
type ObservedItem struct {
	ID   int64
	At   int64
	Body any
}

// Snapshot is the wire form of one stored observation of any type: the
// type-specific measurements are under the field named after the type.
type Snapshot struct {
	ID         int64     `json:"id"`
	ResourceID string    `json:"resourceId"`
	Type       string    `json:"type"`
	ObservedAt time.Time `json:"observedAt"`
	Machine    any       `json:"machine,omitempty"`
	Router     any       `json:"router,omitempty"`
}

// Observations reads and triggers native observation of resources of one
// type. ref is a resource name or "@<id>".
type Observations interface {
	Observe(ctx context.Context, ref string) error
	// History returns the snapshots a query selects, in the order and with
	// the cursor semantics documented on resource.HistoryQuery.
	History(ctx context.Context, ref string, q resource.HistoryQuery) ([]ObservedItem, error)
	// Events returns the events a query selects, in the order and with the
	// cursor semantics documented on resource.EventQuery.
	Events(ctx context.Context, ref string, q resource.EventQuery) ([]ObservedItem, error)
}

// Capability explains whether a durable operation can be submitted now.
type Capability struct {
	Action    string `json:"action"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// Operation is one durable operation; Body is its wire form.
type Operation struct {
	ID         string
	Unresolved bool
	Body       any
}

// Operations submits and reports durable operations (reboot and the like)
// of resources of one type. The API marks the context of every write as a
// direct administrator request (machine.DirectRequest); implementations
// refuse other callers, such as policies.
type Operations interface {
	Capabilities(ctx context.Context, ref string) ([]Capability, error)
	List(ctx context.Context, ref string) ([]Operation, error)
	Get(ctx context.Context, ref, id string) (Operation, error)
	Submit(ctx context.Context, ref string, req resource.OperationRequest) (Operation, error)
	Resolve(ctx context.Context, ref, id, reason string, expected int) (Operation, error)
}

// resourceRef reads the path's resource and its type registration. It has
// written the response on failure.
func (s *Server) resourceRef(w http.ResponseWriter, r *http.Request) (resource.Record, ResourceType, bool) {
	rec, err := s.resources.Get(r.Context(), r.PathValue("resource"))
	if err != nil {
		s.fail(w, r, err, nil)
		return resource.Record{}, ResourceType{}, false
	}
	return rec, s.types[rec.Type], true
}

// writableRef is resourceRef for routes that change a resource or act on it.
// Only the active incarnation of a name can be changed: an archive addressed
// as "@<id>" is read only, and the components resolve writes by name, so
// letting one through would act on the live resource that now carries the
// archive's name. It has written the response on failure.
func (s *Server) writableRef(w http.ResponseWriter, r *http.Request) (resource.Record, ResourceType, bool) {
	rec, reg, ok := s.resourceRef(w, r)
	if !ok {
		return resource.Record{}, ResourceType{}, false
	}
	if rec.Phase == resource.PhaseUnmanaged {
		s.fail(w, r, fmt.Errorf("%w: resource @%s is an unmanaged archive of %q and cannot be changed", resource.ErrPrecondition, rec.ID, rec.Name), nil)
		return resource.Record{}, ResourceType{}, false
	}
	return rec, reg, true
}

type resourceTypeDTO struct {
	Type            string              `json:"type"`
	Adapters        []string            `json:"adapters"`
	NeedsConnection bool                `json:"needsConnection"`
	Description     string              `json:"description"`
	Actions         []resourceActionDTO `json:"actions"`
}

type resourceActionDTO struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Reason explains why the action is not available; empty when it is.
	Reason string `json:"reason"`
	// ReasonMessage is Reason with a code for translation; service reasons
	// are codes themselves and carry none.
	ReasonMessage message.Message `json:"reasonMessage"`
	Risk          string          `json:"risk"`
	Policy        bool            `json:"policy"`
	Kind          string          `json:"kind"`
}

func toActionDTOs(actions []resource.Action) []resourceActionDTO {
	out := make([]resourceActionDTO, 0, len(actions))
	for _, a := range actions {
		out = append(out, resourceActionDTO{
			Name: a.Name, Available: a.Available, Reason: a.Reason, ReasonMessage: resource.ReasonMessage(a.Reason),
			Risk: a.Risk, Policy: a.Policy, Kind: a.Kind,
		})
	}
	return out
}

func (s *Server) handleResourceTypes(w http.ResponseWriter, _ *http.Request) {
	infos := model.ResourceTypes()
	items := make([]resourceTypeDTO, 0, len(infos))
	for _, info := range infos {
		d := resourceTypeDTO{
			Type: info.Type, Adapters: info.Adapters, NeedsConnection: info.NeedsConnection,
			Description: info.Description, Actions: []resourceActionDTO{},
		}
		if c, ok := s.types[info.Type].Type.(ActionCatalog); ok {
			d.Actions = toActionDTOs(c.ActionCatalog())
			// The catalog says what exists, not what is possible now.
			for i := range d.Actions {
				d.Actions[i].Available, d.Actions[i].Reason = false, ""
			}
		}
		items = append(items, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type resourceSummaryDTO struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Adapter    string            `json:"adapter"`
	Phase      string            `json:"phase"`
	Labels     map[string]string `json:"labels"`
	State      string            `json:"state"`
	StateSince *time.Time        `json:"stateSince"`
	Health     string            `json:"health"`
	// Version is the configuration document version.
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	// DeployedVersion is the commit of a service's last successful
	// deployment; empty otherwise.
	DeployedVersion string        `json:"deployedVersion"`
	ManagedBy       *managedByDTO `json:"managedBy"`
	// Relations are the relations the document declares, so that a view of
	// the whole fleet needs no request per resource.
	Relations []model.Relation `json:"relations"`
}

type healthDTO struct {
	Status  string     `json:"status"`
	Since   *time.Time `json:"since"`
	Reasons []string   `json:"reasons"`
	// ReasonMessages are Reasons with codes for translation, in the same
	// order.
	ReasonMessages []message.Message `json:"reasonMessages"`
}

type serviceExtrasDTO struct {
	DeployedVersion string         `json:"deployedVersion"`
	LastDeployment  *deploymentDTO `json:"lastDeployment"`
}

type statusExtrasDTO struct {
	Status any `json:"status"`
}

type resourceDetailDTO struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Adapter    string            `json:"adapter"`
	Connection string            `json:"connection"`
	Phase      string            `json:"phase"`
	Labels     map[string]string `json:"labels"`
	Document   *model.Resource   `json:"document"`
	Version    int               `json:"version"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
	UpdatedBy  actorDTO          `json:"updatedBy"`
	State      string            `json:"state"`
	StateSince *time.Time        `json:"stateSince"`
	Health     healthDTO         `json:"health"`
	Checks     []checkStatusDTO  `json:"checks"`
	Alerts     []monitor.Alert   `json:"alerts"`
	// Actions tells clients what they can do now, so that they do not
	// repeat the server's rules.
	Actions []resourceActionDTO `json:"actions"`
	// Operation is the durable operation whose outcome is still open.
	Operation any           `json:"operation"`
	ManagedBy *managedByDTO `json:"managedBy"`
	// UnresolvedRelations are relation targets that name no resource yet,
	// so a save shows at once which relations do not count.
	UnresolvedRelations []string `json:"unresolvedRelations"`
	// The type-specific parts; only the one of the resource's type is set.
	Service *serviceExtrasDTO `json:"service,omitempty"`
	Machine *statusExtrasDTO  `json:"machine,omitempty"`
	Router  *statusExtrasDTO  `json:"router,omitempty"`
}

func labelsOf(rec resource.Record) map[string]string {
	if rec.Document == nil || rec.Document.Metadata.Labels == nil {
		return map[string]string{}
	}
	return rec.Document.Metadata.Labels
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

// status asks the resource's type for its state. A type that cannot tell
// (an archive without type state, an unregistered type) reports nothing.
func (s *Server) status(ctx context.Context, rec resource.Record) (resource.Status, error) {
	t := s.types[rec.Type].Type
	if t == nil {
		return resource.Status{}, nil
	}
	return t.Status(ctx, rec)
}

// healthOf explains a combined health: the type's own reasons first, then
// the checks that fail.
func healthOf(st monitor.ResourceStatus) healthDTO {
	ms := []message.Message{}
	if st.Native != nil {
		ms = append(ms, resource.ReasonMessages(st.Native.Reasons)...)
	}
	for _, c := range st.Checks {
		if c.Status == monitor.StatusDown {
			ms = append(ms, message.New("health.checkDown", "check "+c.Name+" is down", "check", c.Name))
		}
	}
	return healthDTO{Status: string(st.Health), Since: optTime(st.Since), Reasons: message.Texts(ms), ReasonMessages: ms}
}

func (s *Server) detail(ctx context.Context, r *http.Request, rec resource.Record) (resourceDetailDTO, error) {
	_, v, err := s.resources.GetVersion(ctx, "@"+rec.ID, rec.Version)
	if err != nil {
		return resourceDetailDTO{}, err
	}
	st, err := s.status(ctx, rec)
	if err != nil {
		return resourceDetailDTO{}, err
	}
	out := resourceDetailDTO{
		ID: rec.ID, Name: rec.Name, Type: rec.Type, Adapter: rec.Adapter, Connection: rec.Document.Spec.Connection,
		Phase: rec.Phase, Labels: labelsOf(rec), Document: rec.Document, Version: rec.Version,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, UpdatedBy: toActorDTO(v.Actor),
		State: st.State, StateSince: optTime(st.StateSince),
		Health:  healthDTO{Status: string(monitor.HealthUnknown), Reasons: []string{}, ReasonMessages: []message.Message{}},
		Checks:  []checkStatusDTO{},
		Alerts:  []monitor.Alert{},
		Actions: []resourceActionDTO{},
	}
	reg := s.types[rec.Type]
	if reg.Type != nil {
		out.Actions = toActionDTOs(reg.Type.Actions(ctx, rec))
	}
	switch rec.Type {
	case model.ResourceMachine:
		out.Machine = &statusExtrasDTO{Status: st.Detail}
	case model.ResourceRouter:
		out.Router = &statusExtrasDTO{Status: st.Detail}
	}
	if reg.Operations != nil {
		ops, err := reg.Operations.List(ctx, "@"+rec.ID)
		if err != nil {
			return resourceDetailDTO{}, err
		}
		if i := slices.IndexFunc(ops, func(o Operation) bool { return o.Unresolved }); i >= 0 {
			out.Operation = ops[i].Body
		}
	}
	// Impact reports no relations of an archive.
	imp, err := s.resources.Impact(ctx, "@"+rec.ID)
	if err != nil {
		return resourceDetailDTO{}, err
	}
	out.UnresolvedRelations = imp.Unresolved
	// History and monitoring are keyed by name, which an archive has
	// released to a possible successor.
	if rec.Phase == resource.PhaseUnmanaged {
		return out, nil
	}
	if mst, ok := s.monitor.Resource(rec.Name); ok {
		out.Health = healthOf(mst)
		for _, c := range mst.Checks {
			out.Checks = append(out.Checks, toCheckStatusDTO(c))
		}
	}
	alerts, err := s.monitor.FiringAlerts(ctx, rec.Name)
	if err != nil {
		return resourceDetailDTO{}, err
	}
	if alerts != nil {
		out.Alerts = alerts
	}
	out.ManagedBy = s.managedFor(r, model.KindResource, rec.Name)
	if rec.Type == model.ResourceService {
		deps, err := s.catalog.Deployments(ctx, rec.Name, 20, 0)
		if err != nil {
			return resourceDetailDTO{}, err
		}
		out.Service = &serviceExtrasDTO{DeployedVersion: deployedVersion(deps)}
		if len(deps) > 0 {
			d := toDeploymentDTO(deps[0])
			out.Service.LastDeployment = &d
		}
	}
	return out, nil
}

// writeDetail answers with the current detail of the resource with id.
func (s *Server) writeDetail(w http.ResponseWriter, r *http.Request, id string, status int) {
	rec, err := s.resources.Get(r.Context(), "@"+id)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	d, err := s.detail(r.Context(), r, rec)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(rec.Version))
	writeJSON(w, status, d)
}

func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.resourceRef(w, r)
	if !ok {
		return
	}
	d, err := s.detail(r.Context(), r, rec)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(rec.Version))
	writeJSON(w, http.StatusOK, d)
}

// labelFilter reads the repeated label=key=value parameters. It has written
// the response on failure.
func labelFilter(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	out := map[string]string{}
	for _, raw := range r.URL.Query()["label"] {
		k, v, found := strings.Cut(raw, "=")
		if !found || k == "" {
			writeError(w, http.StatusBadRequest, codeBadRequest, "label filter must be key=value", map[string]any{"param": "label"})
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

// typeParam reads the optional type filter. It has written the response on
// failure.
func typeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	typ := r.URL.Query().Get("type")
	if typ == "" {
		return "", true
	}
	for _, t := range model.ResourceTypes() {
		if t.Type == typ {
			return typ, true
		}
	}
	writeError(w, http.StatusBadRequest, codeBadRequest, "unknown resource type", map[string]any{"param": "type"})
	return "", false
}

func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	typ, ok := typeParam(w, r)
	if !ok {
		return
	}
	labels, ok := labelFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	wantPhase, wantState, wantHealth := q.Get("phase"), q.Get("state"), q.Get("health")
	switch wantPhase {
	case "", resource.PhaseManaged, resource.PhaseRetired:
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "phase must be managed or retired; archives are under /resource-archives",
			map[string]any{"param": "phase"})
		return
	}
	switch monitor.Health(wantHealth) {
	case "", monitor.HealthUnknown, monitor.HealthHealthy, monitor.HealthDegraded, monitor.HealthDown:
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "unknown health", map[string]any{"param": "health"})
		return
	}
	recs, err := s.resources.List(r.Context(), resource.Filter{Type: typ, Labels: labels, IncludeRetired: true})
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	health := map[string]monitor.Health{}
	for _, st := range s.monitor.Resources() {
		health[st.Resource] = st.Health
	}
	type row struct {
		rec resource.Record
		st  resource.Status
	}
	var matched []row
	for _, rec := range recs {
		if wantPhase != "" && rec.Phase != wantPhase {
			continue
		}
		h := health[rec.Name]
		if h == "" {
			h = monitor.HealthUnknown
		}
		if wantHealth != "" && string(h) != wantHealth {
			continue
		}
		st, err := s.status(r.Context(), rec)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		if wantState != "" && st.State != wantState {
			continue
		}
		matched = append(matched, row{rec: rec, st: st})
	}
	page, next, ok := pageSlice(w, p, matched)
	if !ok {
		return
	}
	managed := s.managedIndex(r, model.KindResource)
	items := make([]resourceSummaryDTO, 0, len(page))
	for _, it := range page {
		d := s.summary(it.rec, it.st, health[it.rec.Name], managed[it.rec.Name])
		if it.rec.Type == model.ResourceService {
			deps, err := s.catalog.Deployments(r.Context(), it.rec.Name, 20, 0)
			if err != nil {
				s.fail(w, r, err, nil)
				return
			}
			d.DeployedVersion = deployedVersion(deps)
		}
		items = append(items, d)
	}
	writeList(w, items, next)
}

func (s *Server) summary(rec resource.Record, st resource.Status, h monitor.Health, managed *managedByDTO) resourceSummaryDTO {
	if h == "" {
		h = monitor.HealthUnknown
	}
	return resourceSummaryDTO{
		ID: rec.ID, Name: rec.Name, Type: rec.Type, Adapter: rec.Adapter, Phase: rec.Phase, Labels: labelsOf(rec),
		State: st.State, StateSince: optTime(st.StateSince), Health: string(h), Version: rec.Version,
		UpdatedAt: rec.UpdatedAt, ManagedBy: managed, Relations: relationsOf(rec),
	}
}

// relationsOf returns the relations of the record's document, never nil.
func relationsOf(rec resource.Record) []model.Relation {
	if rec.Document == nil || rec.Document.Spec.Relations == nil {
		return []model.Relation{}
	}
	return slices.Clone(rec.Document.Spec.Relations)
}

func (s *Server) handleResourceArchives(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	typ, ok := typeParam(w, r)
	if !ok {
		return
	}
	recs, err := s.resources.Archives(r.Context(), typ)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, recs)
	if !ok {
		return
	}
	items := make([]resourceSummaryDTO, 0, len(page))
	for _, rec := range page {
		st, err := s.status(r.Context(), rec)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		items = append(items, s.summary(rec, st, monitor.HealthUnknown, nil))
	}
	writeList(w, items, next)
}

// decodeResourceBody reads and decodes a resource document. It has written
// the response on failure.
func (s *Server) decodeResourceBody(w http.ResponseWriter, r *http.Request) ([]byte, *model.Resource, bool) {
	body, ok := readDocument(w, r)
	if !ok {
		return nil, nil, false
	}
	doc, err := model.DecodeResource(body)
	if err != nil {
		s.fail(w, r, err, nil)
		return nil, nil, false
	}
	return body, doc, true
}

func (s *Server) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	body, doc, ok := s.decodeResourceBody(w, r)
	if !ok {
		return
	}
	if _, err := s.resources.Put(r.Context(), body, 0); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	rec, err := s.resources.Get(r.Context(), doc.Metadata.Name)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Location", "/api/v1/resources/"+rec.Name)
	s.writeDetail(w, r, rec.ID, http.StatusCreated)
}

// requiredIfMatch reads an If-Match header that names a version. It has
// written the response on failure.
func requiredIfMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	im, ok := parseIfMatch(w, r)
	if !ok {
		return 0, false
	}
	if !im.present || im.wildcard {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			"If-Match with the current version is required; create resources with POST /resources", nil)
		return 0, false
	}
	return im.version, true
}

// conflictDetails adds the current version to a version conflict.
func (s *Server) conflictDetails(r *http.Request, err error, ref string, expected int) map[string]any {
	if !errors.Is(err, resource.ErrConflict) {
		return nil
	}
	cur, gerr := s.resources.Get(r.Context(), ref)
	if gerr != nil {
		return nil
	}
	return map[string]any{"currentVersion": cur.Version, "expectedVersion": expected}
}

func (s *Server) handlePutResource(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	body, doc, ok := s.decodeResourceBody(w, r)
	if !ok {
		return
	}
	// A name in the URL must match the document before anything is looked
	// up, so a mismatch is a validation error even for an unknown name.
	if path := r.PathValue("resource"); !strings.HasPrefix(path, "@") && doc.Metadata.Name != path {
		writeFieldError(w, "metadata.name", "must equal the resource name in the URL \""+path+"\"")
		return
	}
	rec, _, ok := s.writableRef(w, r)
	if !ok {
		return
	}
	if doc.Metadata.Name != rec.Name {
		writeFieldError(w, "metadata.name", "must equal the resource name in the URL \""+rec.Name+"\"")
		return
	}
	if _, err := s.resources.Put(r.Context(), body, expected); err != nil {
		s.fail(w, r, err, s.conflictDetails(r, err, "@"+rec.ID, expected))
		return
	}
	s.writeDetail(w, r, rec.ID, http.StatusOK)
}

type versionDTO struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy actorDTO  `json:"createdBy"`
	// RestoredFrom is the version this one was restored from, or null.
	RestoredFrom *int `json:"restoredFrom"`
}

func toVersionDTO(v resource.Version) versionDTO {
	d := versionDTO{Version: v.Version, CreatedAt: v.CreatedAt, CreatedBy: toActorDTO(v.Actor)}
	if v.RestoredFrom > 0 {
		d.RestoredFrom = &v.RestoredFrom
	}
	return d
}

func (s *Server) handleResourceVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	vs, err := s.resources.Versions(r.Context(), r.PathValue("resource"))
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

// docMeta is the version metadata every document response carries.
type docMeta struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy actorDTO  `json:"updatedBy"`
}

type resourceVersionDTO struct {
	Document *model.Resource `json:"document"`
	docMeta
	RestoredFrom *int `json:"restoredFrom"`
}

func (s *Server) handleGetResourceVersion(w http.ResponseWriter, r *http.Request) {
	v, ok := pathVersion(w, r, "v")
	if !ok {
		return
	}
	doc, info, err := s.resources.GetVersion(r.Context(), r.PathValue("resource"), v)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := resourceVersionDTO{Document: doc, docMeta: docMeta{Version: v, UpdatedAt: info.CreatedAt, UpdatedBy: toActorDTO(info.Actor)}}
	if info.RestoredFrom > 0 {
		out.RestoredFrom = &info.RestoredFrom
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRestoreResource(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.writableRef(w, r)
	if !ok {
		return
	}
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
	// Without If-Match the restore is based on the version just read.
	expected := rec.Version
	if im.present && !im.wildcard {
		expected = im.version
	}
	if _, err := s.resources.Restore(r.Context(), "@"+rec.ID, req.Version, expected); err != nil {
		s.fail(w, r, err, s.conflictDetails(r, err, "@"+rec.ID, expected))
		return
	}
	s.writeDetail(w, r, rec.ID, http.StatusOK)
}

type phaseRequest struct {
	Phase string `json:"phase"`
	// Version is the document version the change is based on; If-Match
	// may carry it instead.
	Version int    `json:"version"`
	Reason  string `json:"reason"`
	// AcknowledgeImpact accepts the impact of a phase change that acts
	// (retiring a running service stops it).
	AcknowledgeImpact bool `json:"acknowledgeImpact"`
}

// phaseActions names the phase change that leads to each phase, as the
// resource types list it in their actions.
var phaseActions = map[string]string{
	resource.PhaseManaged:   "resume",
	resource.PhaseRetired:   "retire",
	resource.PhaseUnmanaged: "unmanage",
}

// maxPhaseReason bounds the free text stored with a phase change, like the
// reasons of machine maintenance and operations.
const maxPhaseReason = 1000

func (s *Server) handleResourcePhase(w http.ResponseWriter, r *http.Request) {
	rec, reg, ok := s.writableRef(w, r)
	if !ok {
		return
	}
	var req phaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Phase {
	case resource.PhaseManaged, resource.PhaseRetired, resource.PhaseUnmanaged:
	default:
		writeFieldError(w, "phase", "must be managed, retired or unmanaged")
		return
	}
	if len(req.Reason) > maxPhaseReason {
		writeFieldError(w, "reason", fmt.Sprintf("must be at most %d bytes", maxPhaseReason))
		return
	}
	expected := req.Version
	if expected == 0 {
		im, ok := parseIfMatch(w, r)
		if !ok {
			return
		}
		if !im.present || im.wildcard {
			writeFieldError(w, "version", "the current document version is required, in the body or If-Match")
			return
		}
		expected = im.version
	}
	if reg.Type == nil {
		s.fail(w, r, unsupported(rec, "phase control"), nil)
		return
	}
	if a, ok := typeAction(reg, phaseActions[req.Phase], resource.KindPhase); ok {
		if r, ok = s.guardImpact(w, r, rec, a, req.AcknowledgeImpact); !ok {
			return
		}
	}
	if err := reg.Type.ChangePhase(r.Context(), rec, req.Phase, expected, req.Reason); err != nil {
		s.fail(w, r, err, s.conflictDetails(r, err, "@"+rec.ID, expected))
		return
	}
	s.writeDetail(w, r, rec.ID, http.StatusOK)
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
	// Deployments are keyed by name, so an archive addressed as "@<id>"
	// shows the history of its name.
	rec, _, ok := s.resourceRef(w, r)
	if !ok {
		return
	}
	deps, err := s.catalog.Deployments(r.Context(), rec.Name, p.limit+1, before)
	if err != nil {
		s.fail(w, r, err, nil)
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

type deploymentDTO struct {
	ID              int64      `json:"id"`
	Resource        string     `json:"resource"`
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
		ID: d.ID, Resource: d.Resource, Action: d.Action, Version: d.Version, PreviousVersion: d.PreviousVersion,
		StartedAt: d.StartedAt, FinishedAt: optTime(d.FinishedAt), Outcome: d.Outcome, Actor: toActorDTO(d.Actor),
		RunID: d.RunID, Error: d.Error, LogExcerpt: d.LogExcerpt,
	}
}

func (s *Server) listTimeline(w http.ResponseWriter, r *http.Request, name string) {
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
		Resource: name, From: from, To: to, Kinds: kinds, Limit: p.limit, Before: before,
	})
	if err != nil {
		s.fail(w, r, err, nil)
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

func (s *Server) handleResourceTimeline(w http.ResponseWriter, r *http.Request) {
	// The timeline is keyed by name; "@<id>" reads the archive's name.
	rec, _, ok := s.resourceRef(w, r)
	if !ok {
		return
	}
	s.listTimeline(w, r, rec.Name)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	s.listTimeline(w, r, r.URL.Query().Get("resource"))
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
