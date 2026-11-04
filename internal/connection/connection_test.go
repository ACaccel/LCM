package connection_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/secret"
)

const password = "synthetic-router-password"

type secrets map[string]string

func (s secrets) Get(name string) (secret.Value, error) {
	v, ok := s[name]
	if !ok {
		return secret.Value{}, secret.ErrNotFound
	}
	return secret.NewValue(v), nil
}

var testSecrets = secrets{"router-password": password, "empty": ""}

const valid = `connections:
  - name: home-router
    type: xiaomi
    url: http://192.168.31.1
    passwordSecret: router-password
    allowActions: [reboot]
    allowResources: [router]
  - name: lab
    type: xiaomi
    url: https://10.0.0.1/
    username: operator
    passwordSecret: router-password
    deviceId: "02:00:00:00:00:02"
    insecureSkipVerify: true
`

func TestParse(t *testing.T) {
	s, err := connection.Parse([]byte(valid), testSecrets)
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	home, ok := s.Get("home-router")
	if !ok || home.Username != "admin" || home.URL != "http://192.168.31.1" || !home.Allows("reboot") || !home.AllowsResource("router") || home.AllowsResource("other") {
		t.Fatalf("home = %+v", home)
	}
	lab, _ := s.Get("lab")
	if lab.Username != "operator" || lab.Allows("reboot") || !lab.AllowsResource("anything") || !lab.InsecureSkipVerify || lab.DeviceID != "02:00:00:00:00:02" {
		t.Fatalf("lab = %+v", lab)
	}
	list[0].AllowActions[0] = "changed"
	if again, _ := s.Get("home-router"); again.AllowActions[0] != "reboot" {
		t.Fatal("List shares slices with the set")
	}
	if empty, err := connection.Parse(nil, testSecrets); err != nil || len(empty.List()) != 0 {
		t.Fatalf("empty: %v, %v", empty, err)
	}
}

func TestParseRejects(t *testing.T) {
	entry := func(fields string) string {
		return "connections:\n  - name: r\n    type: xiaomi\n    passwordSecret: router-password\n" + fields
	}
	cases := []struct {
		name, doc, want string
	}{
		{"unknown field", entry("    url: http://r\n    password: x\n"), "field password not found"},
		{"duplicate", entry("    url: http://r\n") + "  - name: r\n    type: xiaomi\n    url: http://r\n    passwordSecret: router-password\n", "duplicate name"},
		{"bad name", "connections:\n  - name: Router\n    type: xiaomi\n    url: http://r\n    passwordSecret: router-password\n", "name must be"},
		{"unknown type", "connections:\n  - name: r\n    type: openwrt\n    url: http://r\n    passwordSecret: router-password\n", "unknown type"},
		{"missing url", entry(""), "url is required"},
		{"credentials", entry("    url: http://admin:hunter2@r\n"), "credentials"},
		{"path", entry("    url: http://r/cgi-bin\n"), "path"},
		{"query", entry("    url: http://r/?a=b\n"), "query"},
		{"scheme", entry("    url: ftp://r\n"), "http or https"},
		{"unknown action", entry("    url: http://r\n    allowActions: [shutdown]\n"), "unknown action"},
		{"duplicate action", entry("    url: http://r\n    allowActions: [reboot, reboot]\n"), "duplicate action"},
		{"bad resource", entry("    url: http://r\n    allowResources: [Bad_Name]\n"), "not a resource name"},
		{"device id", entry("    url: http://r\n    deviceId: not-a-mac\n"), "device ID"},
		{"missing secret", "connections:\n  - name: r\n    type: xiaomi\n    url: http://r\n    passwordSecret: absent\n", "cannot be resolved"},
		{"empty secret", "connections:\n  - name: r\n    type: xiaomi\n    url: http://r\n    passwordSecret: empty\n", "is empty"},
		{"two documents", entry("    url: http://r\n") + "---\nconnections: []\n", "single YAML document"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := connection.Parse([]byte(c.doc), testSecrets)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error leaks a credential: %v", err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	if s, err := connection.Load("", testSecrets); err != nil || len(s.List()) != 0 {
		t.Fatalf("no file: %v, %v", s, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := connection.Load(path, testSecrets); err != nil || len(s.List()) != 2 {
		t.Fatalf("load: %v, %v", s, err)
	}
	if err := os.Chmod(path, 0o664); err != nil { //nolint:gosec // the test checks that a group-writable file is refused
		t.Fatal(err)
	}
	if _, err := connection.Load(path, testSecrets); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("group-writable: %v", err)
	}
	if _, err := connection.Load(filepath.Join(dir, "absent.yaml"), testSecrets); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := connection.Load(dir, testSecrets); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestCheck(t *testing.T) {
	s, err := connection.Parse([]byte(valid), testSecrets)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, adapter, resource, want string }{
		{"home-router", "xiaomi", "router", ""},
		{"lab", "xiaomi", "anything", ""},
		{"absent", "xiaomi", "router", "not configured"},
		{"home-router", "other", "router", "of type xiaomi"},
		{"home-router", "xiaomi", "other", "does not allow resource"},
	}
	for _, c := range cases {
		if got := s.Check(c.name, c.adapter, c.resource); (c.want == "" && got != "") || !strings.Contains(got, c.want) {
			t.Errorf("Check(%s, %s, %s) = %q, want %q", c.name, c.adapter, c.resource, got, c.want)
		}
	}
	var none *connection.Set
	if got := none.Check("x", "xiaomi", "r"); got == "" {
		t.Error("nil set allowed a connection")
	}
}
