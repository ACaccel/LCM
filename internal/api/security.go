package api

import (
	"net/http"
	"strconv"

	"github.com/ACaccel/lcm/internal/security"
)

func (s *Server) securityAvailable(w http.ResponseWriter) bool {
	if s.security == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "security integration unavailable", nil)
		return false
	}
	return true
}

func (s *Server) handleSecurityHook(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, security.MaxBodyBytes)
	var e security.Event
	if !decodeJSON(w, r, &e) {
		return
	}
	receipt, err := s.security.Ingest(r.Context(), e)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

func (s *Server) handleSecurityStatus(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	st, err := s.security.Status(r.Context())
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSecurityIncidents(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	before, ok := idCursor(w, p)
	if !ok {
		return
	}
	if !validSecurityQuery(w, r, "machineId|service|source|severity|state|from|to|limit|cursor") {
		return
	}
	from, to, ok := securityTimes(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, err := s.security.ListFiltered(r.Context(), security.IncidentFilter{MachineID: q.Get("machineId"), Service: q.Get("service"), Source: q.Get("source"), Severity: q.Get("severity"), State: q.Get("state"), From: from, To: to}, before, p.limit+1)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	var next *string
	if len(items) > p.limit {
		items = items[:p.limit]
		next = encodeCursor(strconv.FormatInt(items[len(items)-1].Seq, 10))
	}
	enriched := make([]securityIncident, 0, len(items))
	for _, i := range items {
		h, e := s.security.Handling(r.Context(), i.ID)
		if e != nil {
			s.securityFail(w, r, e)
			return
		}
		enriched = append(enriched, securityIncident{Incident: i, Handling: h})
	}
	writeList(w, enriched, next)
}

func (s *Server) handleSecurityTrafficHook(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, security.MaxBodyBytes)
	var observation security.Traffic
	if !decodeJSON(w, r, &observation) {
		return
	}
	receipt, err := s.security.IngestTraffic(r.Context(), observation)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, receipt)
}

func (s *Server) handleSecurityTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	before, ok := idCursor(w, p)
	if !ok {
		return
	}
	if !validSecurityQuery(w, r, "machineId|service|category|from|to|limit|cursor") {
		return
	}
	from, to, ok := securityTimes(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, err := s.security.TrafficFiltered(r.Context(), security.TrafficFilter{MachineID: q.Get("machineId"), Service: q.Get("service"), Category: q.Get("category"), From: from, To: to}, before, p.limit+1)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	var next *string
	if len(items) > p.limit {
		items = items[:p.limit]
		next = encodeCursor(strconv.FormatInt(items[len(items)-1].ID, 10))
	}
	writeList(w, items, next)
}
