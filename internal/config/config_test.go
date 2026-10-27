package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func load(t *testing.T, e map[string]string, args ...string) (Config, error) {
	t.Helper()
	c, err := FromEnv(env(e))
	if err != nil {
		return Config{}, err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "127.0.0.1:7800", DataDir: "./data", LogFormat: "text",
		LogLevel: "info", CookieSecure: false, SessionTTL: 7 * 24 * time.Hour,
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if !c.IsLoopback() {
		t.Error("default listen address must be loopback")
	}
	if got := c.DBPath(); got != filepath.Join("data", "lcm.db") {
		t.Errorf("DBPath = %q", got)
	}
}

func TestEnvOverride(t *testing.T) {
	c, err := load(t, map[string]string{
		EnvListen: "0.0.0.0:9000", EnvDataDir: "/tmp/x", EnvLogFormat: "json",
		EnvLogLevel: "debug", EnvCookieSecure: "true", EnvSessionTTL: "1h",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "0.0.0.0:9000", DataDir: "/tmp/x", LogFormat: "json",
		LogLevel: "debug", CookieSecure: true, SessionTTL: time.Hour,
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if c.IsLoopback() {
		t.Error("0.0.0.0 is not loopback")
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	c, err := load(t,
		map[string]string{EnvListen: "0.0.0.0:9000", EnvLogLevel: "debug", EnvCookieSecure: "true"},
		"--listen", "localhost:1234", "--log-level", "warn", "--cookie-secure=false",
		"--data-dir", "/tmp/y", "--log-format", "json", "--session-ttl", "30m")
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "localhost:1234", DataDir: "/tmp/y", LogFormat: "json",
		LogLevel: "warn", CookieSecure: false, SessionTTL: 30 * time.Minute,
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if !c.IsLoopback() {
		t.Error("localhost is loopback")
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"bad level", map[string]string{EnvLogLevel: "verbose"}, nil, "log level"},
		{"bad format", map[string]string{EnvLogFormat: "xml"}, nil, "log format"},
		{"listen without port", map[string]string{EnvListen: "localhost"}, nil, "listen address"},
		{"listen bad port", map[string]string{EnvListen: "127.0.0.1:99999"}, nil, "invalid port"},
		{"listen non-numeric port", nil, []string{"--listen", "127.0.0.1:http"}, "invalid port"},
		{"empty data dir", nil, []string{"--data-dir", " "}, "data dir"},
		{"bad bool", map[string]string{EnvCookieSecure: "maybe"}, nil, EnvCookieSecure},
		{"bad duration", map[string]string{EnvSessionTTL: "soon"}, nil, EnvSessionTTL},
		{"non-positive ttl", nil, []string{"--session-ttl", "0s"}, "session ttl"},
		{"bad flag level", nil, []string{"--log-level", "loud"}, "log level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.env, tc.args...)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestEnsureDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	c := Config{DataDir: dir}
	if err := c.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("write check left files behind: %v", entries)
	}
}

func TestEnsureDataDirUnwritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file in place of the directory fails regardless of uid.
	if err := (Config{DataDir: file}).EnsureDataDir(); err == nil {
		t.Fatal("expected error for a file used as data dir")
	}
	if err := (Config{DataDir: filepath.Join(file, "sub")}).EnsureDataDir(); err == nil {
		t.Fatal("expected error for a dir below a file")
	}
}

func TestNewLoggerFormats(t *testing.T) {
	var sb strings.Builder
	c := Config{LogFormat: "json", LogLevel: "warn"}
	l := c.NewLogger(&sb)
	l.Info("hidden")
	l.Warn("shown", "k", "v")
	out := sb.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, `"msg":"shown"`) {
		t.Fatalf("unexpected output: %q", out)
	}
}
