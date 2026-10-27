package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/signal"
)

// stream reads server-sent events from a live server.
type stream struct {
	t      *testing.T
	cancel context.CancelFunc
	ch     chan streamEvent
}

type streamEvent struct {
	Name string
	Data map[string]any
}

func (x *site) openStream(query string) *stream {
	x.t.Helper()
	srv := httptest.NewServer(x.handler)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+v1+"/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+x.admin)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req) //nolint:bodyclose // closed by the cleanup registered below
	if err != nil {
		cancel()
		srv.Close()
		x.t.Fatal(err)
	}
	s := &stream{t: x.t, cancel: cancel, ch: make(chan streamEvent, 256)}
	ready := make(chan struct{})
	go func() {
		defer close(s.ch)
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			switch line := sc.Text(); {
			case line == ": connected":
				close(ready)
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var data map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
				s.ch <- streamEvent{Name: name, Data: data}
			}
		}
	}()
	x.t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
		client.CloseIdleConnections()
		srv.CloseClientConnections()
		srv.Close()
	})
	select {
	case <-ready:
	case <-time.After(waitTimeout):
		x.t.Fatal("event stream did not open")
	}
	return s
}

// until returns the first event satisfying pred.
func (s *stream) until(what string, pred func(streamEvent) bool) streamEvent {
	s.t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case e, ok := <-s.ch:
			if !ok {
				s.t.Fatalf("stream ended while waiting for %s", what)
			}
			if pred(e) {
				return e
			}
		case <-timeout:
			s.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestEventStreamCarriesSignalsRunsAndChanges(t *testing.T) {
	x := newSite(t)
	x.putPolicy("echo", manualPolicyNodes)
	all := x.openStream("")
	runOnly := x.openStream("?types=run&policy=echo")
	webOnly := x.openStream("?service=web")

	// Catalog changes, timeline entries and signals for a service.
	x.createService(serviceDoc("web", ""))
	e := all.until("timeline event", func(e streamEvent) bool { return e.Name == "timeline" && e.Data["service"] == "web" })
	if tl := e.Data["data"].(map[string]any); tl["kind"] != "config.changed" || tl["summary"] == "" {
		t.Fatalf("timeline event = %v", e)
	}
	e = all.until("catalog event", func(e streamEvent) bool { return e.Name == "catalog" && e.Data["service"] == "web" })
	if c := e.Data["data"].(map[string]any); c["change"] != "created" || c["service"] != "web" {
		t.Fatalf("catalog event = %v", e)
	}

	x.app.Bus.Publish(signal.Signal{Kind: signal.KindHealth, Service: "web", Source: "monitor", Data: map[string]any{"health": "down"}})
	e = webOnly.until("signal for web", func(e streamEvent) bool { return e.Name == "signal" })
	if sig := e.Data["data"].(map[string]any); sig["kind"] != "health.changed" || e.Data["service"] != "web" {
		t.Fatalf("signal event = %v", e)
	}

	// An action reports its progress.
	id := x.startAction("web", "start", nil)
	all.until("action finished", func(e streamEvent) bool {
		return e.Name == "action" && e.Data["data"].(map[string]any)["id"] == id && e.Data["data"].(map[string]any)["status"] == "succeeded"
	})

	// A run produces run and node events, filtered per stream.
	runID := x.call("POST", v1+"/policies/echo/runs", map[string]any{"mode": "dry", "event": map[string]string{"name": "x"}}).
		want(http.StatusAccepted).obj()["runId"].(string)
	fin := runOnly.until("run finished", func(e streamEvent) bool {
		return e.Name == "run" && e.Data["data"].(map[string]any)["kind"] == "run.finished"
	})
	if fin.Data["run"] != runID || fin.Data["policy"] != "echo" || fin.Data["data"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("run.finished = %v", fin)
	}
	all.until("node event", func(e streamEvent) bool { return e.Name == "node" && e.Data["run"] == runID })

	// The service-filtered stream never saw run events (they carry no
	// service), and the run-only stream never saw anything but runs.
	x.app.Bus.Publish(signal.Signal{Kind: signal.KindHealth, Service: "web", Source: "marker"})
	sawMarker := false
	webOnly.until("marker", func(e streamEvent) bool {
		if e.Name == "run" || e.Name == "node" {
			t.Fatalf("service filter let %s through", e.Name)
		}
		sawMarker = e.Name == "signal" && e.Data["data"].(map[string]any)["source"] == "marker"
		return sawMarker
	})
}
