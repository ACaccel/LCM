package api

import (
	"net/http"
	"strconv"

	"github.com/ACaccel/lcm/internal/securityhost"
)

func (s *Server) hostAvailable(w http.ResponseWriter) bool {
	if s.securityHost == nil {
		writeError(w, 503, codeUnavailable, "security host Machine is not configured", nil)
		return false
	}
	return true
}
func (s *Server) handleSecurityHost(w http.ResponseWriter, r *http.Request) {
	// An unconfigured host is a lasting state, not an outage: the status
	// says so, and clients stop polling instead of retrying a 503.
	if s.securityHost == nil {
		writeJSON(w, 200, securityhost.Unconfigured())
		return
	}
	status, err := s.securityHost.Status(r.Context())
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, 200, status)
}
func (s *Server) handleSecurityHostPreview(w http.ResponseWriter, r *http.Request) {
	if !s.hostAvailable(w) {
		return
	}
	var in securityhost.PreviewRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	preview, err := s.securityHost.Preview(r.Context(), in)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, 200, preview)
}
func (s *Server) handleSecurityHostApply(w http.ResponseWriter, r *http.Request) {
	if !s.hostAvailable(w) {
		return
	}
	var in securityhost.ApplyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	op, err := s.securityHost.Apply(r.Context(), in)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, 202, op)
}
func (s *Server) handleSecurityHostOperations(w http.ResponseWriter, r *http.Request) {
	if !s.hostAvailable(w) {
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
	operations, err := s.securityHost.Operations(r.Context(), before, p.limit+1)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	var next *string
	if len(operations) > p.limit {
		operations = operations[:p.limit]
		next = encodeCursor(strconv.FormatInt(operations[len(operations)-1].Seq, 10))
	}
	writeList(w, operations, next)
}
func (s *Server) handleSecurityHostOperation(w http.ResponseWriter, r *http.Request) {
	if !s.hostAvailable(w) {
		return
	}
	op, err := s.securityHost.Operation(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, 200, op)
}
