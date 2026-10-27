package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/monitor/alert"
	"github.com/ACaccel/lcm/internal/trigger"
)

// maxIngestBody caps the bodies of the ingest endpoints.
const maxIngestBody = 1 << 20

// readIngest reads a capped body. It has written the response on failure.
func readIngest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIngestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "request body too large",
				map[string]any{"limitBytes": maxIngestBody})
			return nil, false
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "cannot read request body", nil)
		return nil, false
	}
	return body, true
}

func authenticated(r *http.Request) bool {
	_, ok := auth.ActorFrom(r.Context())
	return ok
}

func (s *Server) handleAlertHook(w http.ResponseWriter, r *http.Request) {
	body, ok := readIngest(w, r)
	if !ok {
		return
	}
	sigs, err := s.monitor.IngestAlert(r.Context(), r.PathValue("source"), body)
	if errors.Is(err, alert.ErrInvalidPayload) {
		writeError(w, http.StatusBadRequest, codeBadRequest, publicMessage(err), nil)
		return
	}
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"signals": len(sigs)})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, ok := readIngest(w, r)
	if !ok {
		return
	}
	err := s.monitor.Heartbeat(r.Context(), r.PathValue("svc"), r.PathValue("check"), body)
	if errors.Is(err, monitor.ErrNotHeartbeat) {
		writeError(w, http.StatusConflict, codeConflict, publicMessage(err), nil)
		return
	}
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// handleWebhook delivers a generic webhook to the trigger.webhook nodes
// listening on the name. A caller with a token may start any listener; an
// anonymous caller only those that verify an HMAC signature, so the
// signature stands in for the token. Unknown names look the same as bad
// signatures to anonymous callers.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, ok := readIngest(w, r)
	if !ok {
		return
	}
	name, authed := r.PathValue("name"), authenticated(r)
	var (
		ids []string
		err error
	)
	if authed {
		ids, err = s.triggers.Deliver(r.Context(), name, r.Header, body)
	} else {
		ids, err = s.triggers.DeliverSigned(r.Context(), name, r.Header, body)
	}
	switch {
	case errors.Is(err, trigger.ErrBadSignature), errors.Is(err, trigger.ErrNoListener) && !authed:
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "an API token or a valid signature is required", nil)
		return
	case errors.Is(err, trigger.ErrBodyTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "webhook body too large",
			map[string]any{"limitBytes": trigger.MaxWebhookBody})
		return
	case err != nil && len(ids) == 0:
		s.fail(w, r, err, nil)
		return
	case err != nil:
		// Some listeners started; report what happened rather than failing
		// the whole delivery.
		s.log.Warn("webhook delivery partially failed", "hook", name, "error", err, "request_id", requestID(r.Context()))
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runs": ids})
}

// githubPush is the part of GitHub's push event LCM reads.
type githubPush struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	Repo    struct {
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

const zeroSHA = "0000000000000000000000000000000000000000"

// verifyGitHub checks GitHub's X-Hub-Signature-256 header against the
// configured secret.
func (s *Server) verifyGitHub(r *http.Request, body []byte) (bool, error) {
	if s.githubSecret == "" || s.secrets == nil {
		return false, nil
	}
	got := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	sig, err := hex.DecodeString(got)
	if err != nil || len(sig) == 0 {
		return false, nil
	}
	key, err := s.secrets.Get(s.githubSecret)
	if err != nil {
		return false, err
	}
	mac := hmac.New(sha256.New, []byte(key.Reveal()))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil)), nil
}

// handleGitHub turns a GitHub push event into a trigger.git notification.
// The configured webhook secret authenticates GitHub, which cannot send a
// bearer token; without it the call needs a token like any ingest call.
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	body, ok := readIngest(w, r)
	if !ok {
		return
	}
	if !authenticated(r) {
		valid, err := s.verifyGitHub(r, body)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !valid {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "an API token or a valid signature is required", nil)
			return
		}
	}
	event := r.Header.Get("X-GitHub-Event")
	if event != "push" {
		// Pings and every other event are acknowledged so GitHub shows the
		// hook as healthy.
		writeJSON(w, http.StatusAccepted, map[string]any{"event": event, "ignored": true})
		return
	}
	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid push event", nil)
		return
	}
	branch, isBranch := strings.CutPrefix(push.Ref, "refs/heads/")
	if !isBranch || push.Deleted || push.After == "" || push.After == zeroSHA {
		writeJSON(w, http.StatusAccepted, map[string]any{"event": event, "ignored": true})
		return
	}
	repo := push.Repo.CloneURL
	if repo == "" {
		repo = push.Repo.HTMLURL
	}
	if repo == "" {
		repo = push.Repo.SSHURL
	}
	if repo == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "push event names no repository", nil)
		return
	}
	n, err := s.triggers.NotifyPush(r.Context(), repo, branch, push.After)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"event": event, "repository": repo, "branch": branch, "commit": push.After, "matched": n,
	})
}
