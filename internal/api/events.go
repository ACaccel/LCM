package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/events"
)

// Event stream tuning.
const (
	// defaultEventBuffer is each client's queue; a client that falls this
	// far behind is told to resynchronize and disconnected.
	defaultEventBuffer = 256
	// defaultEventHeartbeat keeps idle connections alive through proxies.
	defaultEventHeartbeat = 15 * time.Second
)

// eventEnvelope is the data of every server-sent event.
type eventEnvelope struct {
	ID         uint64    `json:"id"`
	Type       string    `json:"type"`
	Time       time.Time `json:"time"`
	Resource   string    `json:"resource,omitempty"`
	ResourceID string    `json:"resourceId,omitempty"`
	Policy     string    `json:"policy,omitempty"`
	Run        string    `json:"run,omitempty"`
	Data       any       `json:"data"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "the event stream is not available", nil)
		return
	}
	q := r.URL.Query()
	filter := events.Filter{Resource: q.Get("resource"), ResourceID: q.Get("resourceId"), Run: q.Get("run"), Policy: q.Get("policy")}
	for _, raw := range q["types"] {
		for t := range strings.SplitSeq(raw, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !events.Type(t).Valid() {
				writeError(w, http.StatusBadRequest, codeBadRequest, "unknown event type",
					map[string]any{"param": "types", "value": t, "types": events.Types})
				return
			}
			filter.Types = append(filter.Types, events.Type(t))
		}
	}

	// The stream outlives the server's read and write timeouts, which
	// exist to protect ordinary requests.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})

	// Subscribe before the first byte so that nothing published after the
	// client sees the stream open is missed.
	sub := s.hub.Subscribe(filter, s.eventBuffer)
	defer sub.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil || rc.Flush() != nil {
		return
	}

	// The credential was checked once, when the stream opened. A stream may
	// stay open for days, so a session ends it when it expires and every
	// heartbeat checks again that the token or session is still valid.
	actor, _ := auth.ActorFrom(r.Context())
	expired := make(chan struct{})
	if until := credentialExpiry(r.Context()); !until.IsZero() {
		t := s.clock.AfterFunc(until.Sub(s.clock.Now()), func() { close(expired) })
		defer t.Stop()
	}
	beat := make(chan struct{}, 1)
	arm := func() clock.Timer {
		return s.clock.AfterFunc(s.eventHeartbeat, func() {
			select {
			case beat <- struct{}{}:
			default:
			}
		})
	}
	tick := arm()
	defer func() { tick.Stop() }()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-expired:
			return
		case <-beat:
			if !s.stillAdmin(r, actor) {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || rc.Flush() != nil {
				return
			}
			tick = arm()
		case e, ok := <-sub.C:
			if !ok {
				if sub.Overflowed() {
					_, _ = fmt.Fprint(w, "event: overflow\ndata: {}\n\n")
					_ = rc.Flush()
				}
				return
			}
			data, err := json.Marshal(eventEnvelope{
				ID: e.ID, Type: string(e.Type), Time: e.Time.UTC(), Resource: e.Resource, ResourceID: e.ResourceID,
				Policy: e.Policy, Run: e.Run, Data: e.Data,
			})
			if err != nil {
				s.log.Warn("event not serializable", "type", e.Type, "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
