package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// TestHostComponentsEndpoint reads a host tree as the unprivileged server
// does: outdated and unreadable files are reported, nothing is written.
func TestHostComponentsEndpoint(t *testing.T) {
	root := t.TempDir()
	put := func(name, content string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	put(hostsetup.Unit, "an old unit", 0o644)
	secret := put(hostsetup.SecurityHelper, "a helper", 0o644)
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}

	var status api.HostStatusFunc = func() ([]hostsetup.ComponentStatus, error) {
		return hostsetup.Status(hostsetup.Host{FS: hostsetup.OS{Root: root}})
	}
	h := newHarness(t, func(o *testOpts) { o.hostStatus = status })
	_, token, err := h.app.Auth.CreateToken(h.ctx, "host-status", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/host/components", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Items []struct {
			Name, State string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range body.Items {
		got[c.Name] = c.State
	}
	want := map[string]string{"core": "outdated", "machine-power": "not_installed", "security-host": "unknown", "deploy": "not_installed", "ingress": "not_installed"}
	if len(got) != len(want) {
		t.Fatalf("components = %v", got)
	}
	for name, state := range want {
		if name == "security-host" && os.Geteuid() == 0 {
			continue // root reads files whatever their mode
		}
		if got[name] != state {
			t.Errorf("%s = %q, want %q (all: %v)", name, got[name], state, got)
		}
	}
}
