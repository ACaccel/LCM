package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceAddCommand(t *testing.T) {
	var got map[string]any
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/config/sources" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		got = nil
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"name":"main"}`)
	})
	s.cli("", "source", "add", "--name", "main", "--repo", "https://example.com/org/config.git", "--branch", "stable", "--path", "lcm", "--interval", "5m", "--enabled=false").
		want(t, 0, "source add main: request accepted")
	want := map[string]any{"name": "main", "repo": "https://example.com/org/config.git", "branch": "stable", "path": "lcm", "interval": "5m", "enabled": false}
	if len(got) != len(want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, got[k], v)
		}
	}

	// Unset flags are left to the server defaults.
	s.cli("", "source", "add", "--name", "min", "--repo", "r").want(t, 0)
	if _, ok := got["branch"]; ok || got["enabled"] != true {
		t.Errorf("minimal body = %v", got)
	}
	s.cli("", "source", "add", "--name", "x").want(t, 2)
	s.cli("", "source", "add", "--repo", "r").want(t, 2)
	s.cli("", "source", "add", "stray", "--name", "x", "--repo", "r").want(t, 2)
}

func TestSourceDeleteAndRelease(t *testing.T) {
	var last atomic.Value
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		last.Store(r.Method + " " + r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/ghost") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.cli("", "source", "delete", "main").want(t, 0, "source delete main: request accepted")
	if got := last.Load(); got != "DELETE /api/v1/config/sources/main" {
		t.Errorf("delete request = %v", got)
	}
	s.cli("", "source", "release", "resource", "workstation").want(t, 0, "source release resource/workstation: request accepted")
	if got := last.Load(); got != "DELETE /api/v1/config/ownership/resource/workstation" {
		t.Errorf("release request = %v", got)
	}
	s.cli("", "source", "delete", "ghost").want(t, 1)
	s.cli("", "source", "release", "resource", "ghost").want(t, 1)
	s.cli("", "source", "delete").want(t, 2)
	s.cli("", "source", "release", "resource").want(t, 2)
	s.cli("", "source", "release", "service", "web").want(t, 2)
	s.cli("", "source").want(t, 2)
	s.cli("", "source", "frobnicate").want(t, 2)
}

func TestSourceSyncCommand(t *testing.T) {
	var reads atomic.Int32
	var failing atomic.Bool
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/config/sources/main/sync":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"name":"main","syncing":true}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/config/sources/main":
			if reads.Add(1) < 2 {
				_, _ = io.WriteString(w, `{"name":"main","syncing":true}`)
				return
			}
			if failing.Load() {
				_, _ = io.WriteString(w, `{"name":"main","syncing":false,"lastError":"clone failed"}`)
				return
			}
			_, _ = io.WriteString(w, `{"name":"main","syncing":false,"lastCommit":"0123456789abcdef"}`)
		default:
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
	})
	s.cli("", "source", "sync", "main").want(t, 0, "source sync main: request accepted")
	if reads.Load() != 0 {
		t.Fatalf("sync without --wait polled %d times", reads.Load())
	}
	s.cli("", "source", "sync", "main", "--wait").want(t, 0, "source sync main: synced 0123456789ab")
	if reads.Load() != 2 {
		t.Errorf("polled %d times, want 2", reads.Load())
	}
	failing.Store(true)
	reads.Store(0)
	r := s.cli("", "source", "sync", "--wait", "main").want(t, 1)
	if !strings.Contains(r.stderr, "clone failed") {
		t.Errorf("stderr = %q", r.stderr)
	}
	s.cli("", "source", "sync").want(t, 2)
}
