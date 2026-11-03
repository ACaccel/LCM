package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/security"
)

type securityIncident struct {
	security.Incident
	Handling security.Handling `json:"handling"`
}

func (s *Server) securityFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, codeNotFound, "security resource not found", nil)
		return
	}
	s.fail(w, r, err, nil)
}
func securityVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.Header.Get("If-Match")
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		writeError(w, 400, codeBadRequest, "If-Match must be a quoted non-negative version", nil)
		return 0, false
	}
	n, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || n < 0 {
		writeError(w, 400, codeBadRequest, "If-Match must be a quoted non-negative version", nil)
		return 0, false
	}
	return n, true
}
func securityTime(w http.ResponseWriter, r *http.Request, key string) (time.Time, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return time.Time{}, true
	}
	v, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeError(w, 400, codeBadRequest, key+" must be RFC3339", nil)
		return time.Time{}, false
	}
	return v.UTC(), true
}
func securityTimes(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	from, ok := securityTime(w, r, "from")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	to, ok := securityTime(w, r, "to")
	return from, to, ok
}
func (s *Server) handleSecurityIncident(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	id := r.PathValue("id")
	i, err := s.security.IncidentByID(r.Context(), id)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	h, err := s.security.Handling(r.Context(), id)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	history, err := s.security.HandlingHistory(r.Context(), id)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(h.Version, 10)+`"`)
	writeJSON(w, 200, map[string]any{"incident": securityIncident{Incident: i, Handling: h}, "history": history})
}
func (s *Server) handleSecurityHandling(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	version, ok := securityVersion(w, r)
	if !ok {
		return
	}
	var u security.HandlingUpdate
	if !decodeJSON(w, r, &u) {
		return
	}
	h, err := s.security.UpdateHandling(r.Context(), r.PathValue("id"), version, u)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(h.Version, 10)+`"`)
	writeJSON(w, 200, h)
}
func (s *Server) handleSecuritySettings(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	st, err := s.security.Settings(r.Context())
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(st.Version, 10)+`"`)
	writeJSON(w, 200, st)
}
func (s *Server) handleSecurityPutSettings(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	version, ok := securityVersion(w, r)
	if !ok {
		return
	}
	var u security.SettingsUpdate
	if !decodeJSON(w, r, &u) {
		return
	}
	st, err := s.security.UpdateSettings(r.Context(), version, u)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(st.Version, 10)+`"`)
	writeJSON(w, 200, st)
}
func (s *Server) handleSecurityTest(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	var u security.TestRequest
	if !decodeJSON(w, r, &u) {
		return
	}
	st, err := s.security.StartNotificationTest(r.Context(), u)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	writeJSON(w, 202, st)
}
func (s *Server) handleSecurityGetTest(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	st, err := s.security.NotificationTest(r.Context(), r.PathValue("id"))
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}
func (s *Server) handleSecuritySeries(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	if !validSecurityQuery(w, r, "machineId|service|category|from|to|bucketSeconds") {
		return
	}
	from, to, ok := securityTimes(w, r)
	if !ok {
		return
	}
	if to.IsZero() {
		to = s.now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	bucket := 60
	raw := r.URL.Query().Get("bucketSeconds")
	if raw != "" {
		var err error
		bucket, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, 400, codeBadRequest, "invalid bucketSeconds", nil)
			return
		}
	}
	q := r.URL.Query()
	items, err := s.security.TrafficSeries(r.Context(), security.TrafficFilter{MachineID: q.Get("machineId"), Service: q.Get("service"), Category: q.Get("category"), From: from, To: to}, bucket)
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "from": from, "to": to, "bucketSeconds": bucket})
}
func validSecurityQuery(w http.ResponseWriter, r *http.Request, allowed string) bool {
	for key, values := range r.URL.Query() {
		if !strings.Contains("|"+allowed+"|", "|"+key+"|") || len(values) != 1 {
			writeError(w, 400, codeBadRequest, "unknown or repeated security query parameter", nil)
			return false
		}
	}
	return true
}

func (s *Server) handleSecuritySettingsHistory(w http.ResponseWriter, r *http.Request) {
	if !s.securityAvailable(w) {
		return
	}
	items, err := s.security.SettingsHistory(r.Context())
	if err != nil {
		s.securityFail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
