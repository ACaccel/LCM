package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/events"
)

// sseClient reads a server-sent event stream.
type sseClient struct {
	t      *testing.T
	cancel context.CancelFunc
	resp   *http.Response
	lines  chan string
}

func openStream(t *testing.T, url string, header http.Header) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req) //nolint:bodyclose // closed by sseClient.close, registered as a cleanup
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseClient{t: t, cancel: cancel, resp: resp, lines: make(chan string, 256)}
	go func() {
		defer close(c.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	t.Cleanup(c.close)
	return c
}

func (c *sseClient) close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

func (c *sseClient) line() string {
	c.t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			c.t.Fatal("stream ended")
		}
		return l
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for stream data")
	}
	return ""
}

// event reads the next event frame, skipping comments, and returns its name
// and data.
func (c *sseClient) event() (name string, data map[string]any) {
	c.t.Helper()
	for {
		l := c.line()
		switch {
		case strings.HasPrefix(l, ":"), l == "", strings.HasPrefix(l, "id:"):
		case strings.HasPrefix(l, "event: "):
			name = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &data); err != nil {
				c.t.Fatalf("bad data %q: %v", l, err)
			}
			return name, data
		}
	}
}

func newStreamEnv(t *testing.T) (*env, *events.Hub, *httptest.Server, http.Header, chan struct{}) {
	t.Helper()
	hub := events.NewHub(nil, nil)
	t.Cleanup(hub.Close)
	e := newEnv(t, func(d *Deps) { d.Events, d.EventHeartbeat, d.EventBuffer = hub, 5*time.Millisecond, 2 })
	// handlerDone closes when a request handler returns.
	var once sync.Once
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.handler.ServeHTTP(w, r)
		once.Do(func() { close(handlerDone) })
	}))
	t.Cleanup(srv.Close)
	cookie := e.login()
	h := http.Header{}
	h.Set("Cookie", cookie.Name+"="+cookie.Value)
	return e, hub, srv, h, handlerDone
}

func TestEventStreamFiltersHeartbeatsAndEnds(t *testing.T) {
	e, hub, srv, h, handlerDone := newStreamEnv(t)
	_, ingest, err := e.auth.CreateToken(t.Context(), "hook", "ingest")
	if err != nil {
		t.Fatal(err)
	}

	// Authorization and parameters are checked before streaming starts.
	wantError(t, e.do("GET", "/api/v1/events", nil), http.StatusUnauthorized, codeUnauthenticated)
	wantError(t, e.do("GET", "/api/v1/events", nil, withBearer(ingest)), http.StatusForbidden, codeForbidden)
	wantError(t, e.do("GET", "/api/v1/events?types=signal,bogus", nil, withSession(e.login())), http.StatusBadRequest, codeBadRequest)

	c := openStream(t, srv.URL+"/api/v1/events?service=web&types=signal,action", h)
	if ct := c.resp.Header.Get("Content-Type"); ct != "text/event-stream" || c.resp.StatusCode != http.StatusOK {
		t.Fatalf("response = %d %q", c.resp.StatusCode, ct)
	}
	if l := c.line(); l != ": connected" {
		t.Fatalf("first line = %q", l)
	}

	// The stream is subscribed once the first line arrived, so these are
	// not lost; the filtered ones never show up.
	hub.Publish(events.Event{Type: events.TypeSignal, Service: "db", Data: map[string]any{"n": 0}})
	hub.Publish(events.Event{Type: events.TypeRun, Service: "web", Data: map[string]any{"n": 1}})
	hub.Publish(events.Event{Type: events.TypeSignal, Service: "web", Run: "r1", Data: map[string]any{"n": 2}})
	hub.Publish(events.Event{Type: events.TypeAction, Service: "web", Data: map[string]any{"n": 3}})

	name, data := c.event()
	if name != "signal" || data["type"] != "signal" || data["service"] != "web" || data["run"] != "r1" ||
		data["data"].(map[string]any)["n"] != float64(2) || data["id"] == nil || data["time"] == nil {
		t.Fatalf("first event = %s %v", name, data)
	}
	if name, data = c.event(); name != "action" || data["data"].(map[string]any)["n"] != float64(3) {
		t.Fatalf("second event = %s %v", name, data)
	}

	// Idle connections get keep-alive comments.
	sawBeat := false
	for range 50 {
		if c.line() == ": heartbeat" {
			sawBeat = true
			break
		}
	}
	if !sawBeat {
		t.Fatal("no heartbeat comment")
	}

	// A disconnecting client releases its subscription.
	c.close()
	select {
	case <-handlerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return after the client left")
	}
	if n := hub.Subscribers(); n != 0 {
		t.Fatalf("subscribers after disconnect = %d", n)
	}
}

// blockedWriter is a response writer whose Write blocks until released, as
// a client that stopped reading would block the server.
type blockedWriter struct {
	header  http.Header
	release chan struct{}
	mu      sync.Mutex
	body    strings.Builder
	first   chan struct{}
	once    sync.Once
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) Flush()              {}
func (w *blockedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.first) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *blockedWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestSlowEventClientReceivesOverflowAndIsClosed(t *testing.T) {
	e, hub, _, _, _ := newStreamEnv(t)
	cookie := e.login()
	w := &blockedWriter{header: http.Header{}, release: make(chan struct{}), first: make(chan struct{})}
	r := httptest.NewRequest("GET", "/api/v1/events", nil)
	r.AddCookie(cookie)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.handler.ServeHTTP(w, r)
	}()

	// The handler is stuck writing its first line, so nothing drains the
	// client queue (two slots) and the third event overflows it.
	<-w.first
	for i := range 5 {
		hub.Publish(events.Event{Type: events.TypeSignal, Data: i})
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not end after overflow")
	}
	body := w.text()
	if !strings.HasSuffix(body, "event: overflow\ndata: {}\n\n") {
		t.Fatalf("stream = %q", body)
	}
	if strings.Count(body, "event: signal") != 2 {
		t.Fatalf("expected the two queued events before the overflow notice: %q", body)
	}
	if n := hub.Subscribers(); n != 0 {
		t.Fatalf("subscribers = %d", n)
	}
}
