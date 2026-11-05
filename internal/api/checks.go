package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/monitor"
)

// resourceChecks returns the monitor's view of an active resource of any
// type. A resource without checks or native health has no monitor entry;
// that is an empty result, not a missing resource.
func (s *Server) resourceChecks(w http.ResponseWriter, r *http.Request) (monitor.ResourceStatus, bool) {
	name := r.PathValue("resource")
	rec, err := s.resources.Get(r.Context(), name)
	if err != nil {
		s.fail(w, r, err, nil)
		return monitor.ResourceStatus{}, false
	}
	st, ok := s.monitor.Resource(rec.Name)
	if !ok {
		return monitor.ResourceStatus{Resource: rec.Name, Type: rec.Type, Health: monitor.HealthUnknown}, true
	}
	return st, true
}

type checksDTO struct {
	Resource string           `json:"resource"`
	Health   string           `json:"health"`
	Since    *time.Time       `json:"since"`
	Checks   []checkStatusDTO `json:"checks"`
}

func (s *Server) handleResourceChecks(w http.ResponseWriter, r *http.Request) {
	st, ok := s.resourceChecks(w, r)
	if !ok {
		return
	}
	out := checksDTO{Resource: st.Resource, Health: string(st.Health), Since: optTime(st.Since), Checks: []checkStatusDTO{}}
	for _, c := range st.Checks {
		out.Checks = append(out.Checks, toCheckStatusDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

type pointDTO struct {
	Time         time.Time `json:"time"`
	Count        int64     `json:"count"`
	Failures     int64     `json:"failures"`
	AvgLatencyMs float64   `json:"avgLatencyMs"`
	MinLatencyMs float64   `json:"minLatencyMs"`
	MaxLatencyMs float64   `json:"maxLatencyMs"`
}

type seriesDTO struct {
	Resource string    `json:"resource"`
	Check    string    `json:"check"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	// Source is raw or rollup.
	Source string `json:"source"`
	// StepMs is the bucket width; 0 when every point is one raw result.
	StepMs int64      `json:"stepMs"`
	Points []pointDTO `json:"points"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// defaultSeriesRange is the range served when from is omitted.
const defaultSeriesRange = time.Hour

func (s *Server) handleCheckSeries(w http.ResponseWriter, r *http.Request) {
	st, ok := s.resourceChecks(w, r)
	if !ok {
		return
	}
	check := r.PathValue("check")
	found := false
	for _, c := range st.Checks {
		found = found || c.Name == check
	}
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, "check not found", nil)
		return
	}
	to, ok := timeParam(w, r, "to")
	if !ok {
		return
	}
	if to.IsZero() {
		to = s.now()
	}
	from, ok := timeParam(w, r, "from")
	if !ok {
		return
	}
	if from.IsZero() {
		from = to.Add(-defaultSeriesRange)
	}
	points, ok := intParam(w, r, "points", monitor.DefaultSeriesPoints, 1, monitor.MaxSeriesPoints)
	if !ok {
		return
	}
	ser, err := s.monitor.Series(r.Context(), st.Resource, check, from, to, int(points))
	if errors.Is(err, monitor.ErrInvalidRange) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "from must be before to", map[string]any{"param": "from"})
		return
	}
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := seriesDTO{
		Resource: st.Resource, Check: check, From: ser.From.UTC(), To: ser.To.UTC(), Source: ser.Source,
		StepMs: ser.Step.Milliseconds(), Points: make([]pointDTO, 0, len(ser.Points)),
	}
	for _, p := range ser.Points {
		out.Points = append(out.Points, pointDTO{
			Time: p.Time.UTC(), Count: p.Count, Failures: p.Failures,
			AvgLatencyMs: ms(p.AvgLatency), MinLatencyMs: ms(p.MinLatency), MaxLatencyMs: ms(p.MaxLatency),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type summaryDTO struct {
	Count        int64   `json:"count"`
	Failures     int64   `json:"failures"`
	Uptime       float64 `json:"uptime"`
	AvgLatencyMs float64 `json:"avgLatencyMs"`
	MinLatencyMs float64 `json:"minLatencyMs"`
	MaxLatencyMs float64 `json:"maxLatencyMs"`
}

func toSummaryDTO(m monitor.Summary) summaryDTO {
	return summaryDTO{
		Count: m.Count, Failures: m.Failures, Uptime: m.Uptime,
		AvgLatencyMs: ms(m.AvgLatency), MinLatencyMs: ms(m.MinLatency), MaxLatencyMs: ms(m.MaxLatency),
	}
}

type resourceSummaryRespDTO struct {
	Resource string `json:"resource"`
	Window   string `json:"window"`
	summaryDTO
	Checks map[string]summaryDTO `json:"checks"`
}

// summaryWindows are the windows the summary endpoint accepts.
var summaryWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func (s *Server) handleResourceSummary(w http.ResponseWriter, r *http.Request) {
	st, ok := s.resourceChecks(w, r)
	if !ok {
		return
	}
	label := r.URL.Query().Get("window")
	if label == "" {
		label = "24h"
	}
	window, known := summaryWindows[label]
	if !known {
		writeError(w, http.StatusBadRequest, codeBadRequest, "window must be 24h, 7d or 30d", map[string]any{"param": "window"})
		return
	}
	total, err := s.monitor.ResourceSummary(r.Context(), st.Resource, window)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := resourceSummaryRespDTO{Resource: st.Resource, Window: label, summaryDTO: toSummaryDTO(total), Checks: map[string]summaryDTO{}}
	for _, c := range st.Checks {
		sum, err := s.monitor.CheckSummary(r.Context(), st.Resource, c.Name, window)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		out.Checks[c.Name] = toSummaryDTO(sum)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCheckTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.monitor.CheckTypes()})
}

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	state := monitor.AlertState(r.URL.Query().Get("state"))
	if state == "" {
		state = monitor.AlertsFiring
	}
	if !state.Valid() {
		writeError(w, http.StatusBadRequest, codeBadRequest, "state must be firing, resolved or all", map[string]any{"param": "state"})
		return
	}
	alerts, err := s.monitor.ListAlerts(r.Context(), state, r.URL.Query().Get("resource"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, alerts)
	if !ok {
		return
	}
	writeList(w, page, next)
}
