package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	if want := Default(); !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
	if c.Listen != "127.0.0.1:7800" || c.DataDir != "./data" || c.SessionTTL != 7*24*time.Hour {
		t.Errorf("unexpected core defaults: %+v", c)
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
		EnvLogLevel: "debug", EnvCookieSecure: "true", EnvSessionTTL: "1h", EnvEngineWorkers: "8",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Listen, want.DataDir, want.LogFormat, want.LogLevel = "0.0.0.0:9000", "/tmp/x", "json", "debug"
	want.CookieSecure, want.SessionTTL, want.EngineWorkers = true, time.Hour, 8
	if !reflect.DeepEqual(c, want) {
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
		"--data-dir", "/tmp/y", "--log-format", "json", "--session-ttl", "30m", "--engine-workers", "3")
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Listen, want.DataDir, want.LogFormat, want.LogLevel = "localhost:1234", "/tmp/y", "json", "warn"
	want.SessionTTL, want.EngineWorkers = 30*time.Minute, 3
	if !reflect.DeepEqual(c, want) {
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
		{"bad workers", map[string]string{EnvEngineWorkers: "many"}, nil, EnvEngineWorkers},
		{"negative workers", nil, []string{"--engine-workers", "-1"}, "engine workers"},
		{"too many workers", nil, []string{"--engine-workers", "5000"}, "engine workers"},
		{"bad lock ttl", map[string]string{EnvLockTTL: "forever"}, nil, EnvLockTTL},
		{"zero lock ttl", nil, []string{"--lock-ttl", "0s"}, "lock ttl"},
		{"huge lock ttl", nil, []string{"--lock-ttl", "9999h"}, "lock ttl"},
		{"bad concurrency", map[string]string{EnvMonitorConcurrency: "lots"}, nil, EnvMonitorConcurrency},
		{"zero concurrency", nil, []string{"--monitor-concurrency", "0"}, "monitor concurrency"},
		{"rollup shorter than raw", nil, []string{"--monitor-raw-retention", "48h", "--monitor-rollup-retention", "24h"}, "rollup retention"},
		{"negative raw retention", nil, []string{"--monitor-raw-retention=-1h"}, "raw retention"},
		{"empty alert labels", map[string]string{EnvAlertServiceLabels: " , "}, nil, "alert service labels"},
		{"zero timeline retention", nil, []string{"--timeline-retention", "0s"}, "timeline retention"},
		{"zero run keep", nil, []string{"--run-retention-keep", "0"}, "run retention keep"},
		{"zero run age", map[string]string{EnvRunRetentionAge: "0s"}, nil, "run retention age"},
		{"zero prune interval", nil, []string{"--prune-interval", "0s"}, "prune interval"},
		{"zero action timeout", nil, []string{"--runtime-action-timeout", "0s"}, "runtime action timeout"},
		{"empty git", nil, []string{"--git", " "}, "git binary"},
		{"bad github secret name", map[string]string{EnvGitHubWebhookSecret: "../etc"}, nil, "github webhook secret"},
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

func TestComponentSettings(t *testing.T) {
	c, err := load(t, map[string]string{
		EnvSecretsDir: "/s", EnvCalendarDir: "/c", EnvSystemctl: "/bin/sc", EnvDocker: "/bin/dk", EnvGit: "/bin/git",
		EnvSystemctlPrivilege: "doas", EnvRuntimeStatusTimeout: "3s", EnvRuntimeActionTimeout: "2m", EnvLockTTL: "30m",
		EnvMonitorConcurrency: "2", EnvMonitorRawRetention: "24h", EnvMonitorRollupRetention: "48h",
		EnvAlertServiceLabels: "svc, app ,", EnvTimelineRetention: "720h", EnvRunRetentionKeep: "5",
		EnvRunRetentionAge: "48h", EnvPruneInterval: "10m", EnvGitHubWebhookSecret: "gh-hook",
	}, "--git", "/usr/bin/git", "--alert-service-labels", "a,b")
	if err != nil {
		t.Fatal(err)
	}
	if c.SecretsPath() != "/s" || c.CalendarPath() != "/c" || c.Systemctl != "/bin/sc" || c.Docker != "/bin/dk" {
		t.Errorf("paths: %+v", c)
	}
	if c.Git != "/usr/bin/git" {
		t.Errorf("flag must override env, git = %q", c.Git)
	}
	if !reflect.DeepEqual(c.AlertServiceLabels, []string{"a", "b"}) {
		t.Errorf("alert labels = %v", c.AlertServiceLabels)
	}
	if c.GitHubWebhookSecret != "gh-hook" {
		t.Errorf("github webhook secret = %q", c.GitHubWebhookSecret)
	}
	if !reflect.DeepEqual(c.Privilege(), []string{"doas"}) {
		t.Errorf("privilege = %v", c.Privilege())
	}
	if c.LockTTL != 30*time.Minute || c.MonitorConcurrency != 2 || c.RunRetentionKeep != 5 || c.PruneInterval != 10*time.Minute {
		t.Errorf("values: %+v", c)
	}

	env, err := FromEnv(func(k string) string {
		if k == EnvAlertServiceLabels {
			return "x,y"
		}
		return ""
	})
	if err != nil || !reflect.DeepEqual(env.AlertServiceLabels, []string{"x", "y"}) {
		t.Errorf("env alert labels = %v, %v", env.AlertServiceLabels, err)
	}
}

func TestPrivilege(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"sudo -n", []string{"sudo", "-n"}},
		{"", []string{}},
		{"  ", []string{}},
		{"none", []string{}},
		{"NONE", []string{}},
	}
	for _, tc := range tests {
		got := Config{SystemctlPrivilege: tc.in}.Privilege()
		if got == nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Privilege(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	if got := Default().Privilege(); !reflect.DeepEqual(got, []string{"sudo", "-n"}) {
		t.Errorf("default privilege = %v", got)
	}
}

func TestServeDirs(t *testing.T) {
	data := filepath.Join(t.TempDir(), "d")
	c := Default()
	c.DataDir = data
	if c.SecretsPath() != filepath.Join(data, "secrets") || c.CalendarPath() != filepath.Join(data, "calendars") {
		t.Fatalf("defaults: %q %q", c.SecretsPath(), c.CalendarPath())
	}
	if err := c.EnsureServeDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(c.SecretsPath())
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir: %v %v", st, err)
	}
	if _, err := os.Stat(c.CalendarPath()); err != nil {
		t.Error(err)
	}
	// Clones of config sources stay inside the data directory.
	if st, err := os.Stat(c.SourcesPath()); err != nil || c.SourcesPath() != filepath.Join(data, "sources") || st.Mode().Perm() != 0o700 {
		t.Errorf("sources dir: %q %v %v", c.SourcesPath(), st, err)
	}
}

func TestMissingBinaries(t *testing.T) {
	c := Default()
	c.Systemctl, c.Docker, c.Git = "/nonexistent/systemctl", "/bin/sh", "/nonexistent/git"
	missing := c.MissingBinaries()
	if len(missing) != 2 || !strings.HasPrefix(missing[0], "systemctl") || !strings.HasPrefix(missing[1], "git") {
		t.Errorf("missing = %v", missing)
	}
}
