package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/monitor"
)

// serviceChecks returns the monitor's view of a service that exists in the
// catalog. A service without checks has no monitor entry; that is an empty
// result, not a missing service.
func (s *Server) serviceChecks(w http.ResponseWriter, r *http.Request) (monitor.ServiceStatus, bool) {
	name := r.PathValue("svc")
	if _, err := s.catalog.Get(r.Context(), name); err != nil {
		s.fail(w, r, err, nil)
		return monitor.ServiceStatus{}, false
	}
	st, ok := s.monitor.Service(name)
	if !ok {
		return monitor.ServiceStatus{Service: name, Health: monitor.HealthUnknown}, true
	}
	return st, true
}

type checksDTO struct {
	Service string           `json:"service"`
	Health  string           `json:"health"`
	Since   *time.Time       `json:"since"`
	Checks  []checkStatusDTO `json:"checks"`
}

func (s *Server) handleServiceChecks(w http.ResponseWriter, r *http.Request) {
	st, ok := s.serviceChecks(w, r)
	if !ok {
		return
	}
	out := checksDTO{Service: st.Service, Health: string(st.Health), Since: optTime(st.Since), Checks: []checkStatusDTO{}}
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
	Service string    `json:"service"`
	Check   string    `json:"check"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
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
	st, ok := s.serviceChecks(w, r)
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
	ser, err := s.monitor.Series(r.Context(), st.Service, check, from, to, int(points))
	if errors.Is(err, monitor.ErrInvalidRange) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "from must be before to", map[string]any{"param": "from"})
		return
	}
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := seriesDTO{
		Service: st.Service, Check: check, From: ser.From.UTC(), To: ser.To.UTC(), Source: ser.Source,
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

type serviceSummaryRespDTO struct {
	Service string `json:"service"`
	Window  string `json:"window"`
	summaryDTO
	Checks map[string]summaryDTO `json:"checks"`
}

// summaryWindows are the windows the summary endpoint accepts.
var summaryWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func (s *Server) handleServiceSummary(w http.ResponseWriter, r *http.Request) {
	st, ok := s.serviceChecks(w, r)
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
	total, err := s.monitor.ServiceSummary(r.Context(), st.Service, window)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out := serviceSummaryRespDTO{Service: st.Service, Window: label, summaryDTO: toSummaryDTO(total), Checks: map[string]summaryDTO{}}
	for _, c := range st.Checks {
		sum, err := s.monitor.CheckSummary(r.Context(), st.Service, c.Name, window)
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
	var (
		alerts []monitor.Alert
		err    error
	)
	if svc := r.URL.Query().Get("service"); svc != "" {
		alerts, err = s.monitor.FiringAlerts(r.Context(), svc)
	} else {
		alerts, err = s.monitor.AllFiringAlerts(r.Context())
	}
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
