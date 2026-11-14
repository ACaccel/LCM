package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetBackupsCommand(t *testing.T) {
	const page = `{"items":[
{"id":3,"resource":"web","kind":"backup","backupId":"20261110T020000Z","startedAt":"2026-11-10T02:00:00Z","outcome":"succeeded","size":3145728,"restorable":true},
{"id":2,"resource":"web","kind":"restore","backupId":"20261109T020000Z","startedAt":"2026-11-09T02:00:00Z","outcome":"failed","size":0,"restorable":false}],"nextCursor":null}`
	s := syntheticServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/resources/web/backups" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, page)
	})
	r := s.cli("", "get", "backups", "web").want(t, 0, "BACKUP ID", "RESTORABLE", "SIZE", "20261110T020000Z", "3.0 MiB", "true", "failed")
	rows := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(rows) != 3 || !strings.HasSuffix(rows[2], "false       -") {
		t.Errorf("table:\n%s", r.stdout)
	}
	s.cli("", "get", "backups", "web", "-o", "json").want(t, 0, `"backupId": "20261110T020000Z"`)
	s.cli("", "get", "backups", "web", "-o", "yaml").want(t, 0, "backupId: 20261110T020000Z")
	s.cli("", "get", "backups").want(t, 2)
	s.cli("", "get", "backups", "web", "extra").want(t, 2)
	s.cli("", "get", "backups", "web", "--type", "service").want(t, 2)
}
