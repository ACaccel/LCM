// Package config loads and validates runtime configuration from environment
// variables and command-line flags. Flags take precedence over the
// environment. Secrets are never part of the configuration.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/secret"
)

// Environment variable names.
const (
	EnvListen        = "LCM_LISTEN"
	EnvDataDir       = "LCM_DATA_DIR"
	EnvLogFormat     = "LCM_LOG_FORMAT"
	EnvLogLevel      = "LCM_LOG_LEVEL"
	EnvCookieSecure  = "LCM_COOKIE_SECURE"
	EnvSessionTTL    = "LCM_SESSION_TTL"
	EnvEngineWorkers = "LCM_ENGINE_WORKERS"

	EnvSecretsDir             = "LCM_SECRETS_DIR" //nolint:gosec // names an environment variable, not a credential
	EnvCalendarDir            = "LCM_CALENDAR_DIR"
	EnvSystemctl              = "LCM_SYSTEMCTL"
	EnvSystemctlPrivilege     = "LCM_SYSTEMCTL_PRIVILEGE"
	EnvDocker                 = "LCM_DOCKER"
	EnvGit                    = "LCM_GIT"
	EnvRuntimeStatusTimeout   = "LCM_RUNTIME_STATUS_TIMEOUT"
	EnvRuntimeActionTimeout   = "LCM_RUNTIME_ACTION_TIMEOUT"
	EnvLockTTL                = "LCM_LOCK_TTL"
	EnvMonitorConcurrency     = "LCM_MONITOR_CONCURRENCY"
	EnvMonitorRawRetention    = "LCM_MONITOR_RAW_RETENTION"
	EnvMonitorRollupRetention = "LCM_MONITOR_ROLLUP_RETENTION"
	EnvAlertServiceLabels     = "LCM_ALERT_SERVICE_LABELS"
	EnvTimelineRetention      = "LCM_TIMELINE_RETENTION"
	EnvRunRetentionKeep       = "LCM_RUN_RETENTION_KEEP"
	EnvRunRetentionAge        = "LCM_RUN_RETENTION_AGE"
	EnvPruneInterval          = "LCM_PRUNE_INTERVAL"
	EnvGitHubWebhookSecret    = "LCM_GITHUB_WEBHOOK_SECRET" //nolint:gosec // names an environment variable, not a credential
)

// Defaults.
const (
	DefaultListen     = "127.0.0.1:7800"
	DefaultDataDir    = "./data"
	DefaultLogFormat  = "text"
	DefaultLogLevel   = "info"
	DefaultSessionTTL = 7 * 24 * time.Hour

	// MaxEngineWorkers bounds the policy engine worker pool.
	MaxEngineWorkers = 1024

	// DBFileName is the SQLite file inside the data directory.
	DBFileName = "lcm.db"

	// SecretsDirName and CalendarDirName are the default directories inside
	// the data directory.
	SecretsDirName  = "secrets"
	CalendarDirName = "calendars"
	// SourcesDirName holds LCM's own clones of config sources.
	SourcesDirName = "sources"

	// NoPrivilege disables the systemctl privilege prefix. An empty value
	// does the same in a flag, but an empty environment variable counts as
	// unset, so the environment needs an explicit word.
	NoPrivilege = "none"

	// MaxMonitorConcurrency bounds simultaneously running health probes.
	MaxMonitorConcurrency = 1024

	DefaultSystemctlPrivilege     = "sudo -n"
	DefaultRuntimeStatusTimeout   = 15 * time.Second
	DefaultRuntimeActionTimeout   = 5 * time.Minute
	DefaultLockTTL                = time.Hour
	DefaultMonitorConcurrency     = 8
	DefaultMonitorRawRetention    = 7 * 24 * time.Hour
	DefaultMonitorRollupRetention = 90 * 24 * time.Hour
	DefaultTimelineRetention      = 90 * 24 * time.Hour
	DefaultRunRetentionKeep       = 200
	DefaultRunRetentionAge        = 30 * 24 * time.Hour
	DefaultPruneInterval          = time.Hour
)

// DefaultAlertServiceLabels name the alert labels that map an external alert
// to a service.
var DefaultAlertServiceLabels = []string{"lcm_service", "service"}

// Config is the validated runtime configuration.
type Config struct {
	Listen       string
	DataDir      string
	LogFormat    string
	LogLevel     string
	CookieSecure bool
	SessionTTL   time.Duration
	// EngineWorkers is the policy engine worker pool size; 0 selects
	// max(GOMAXPROCS*2, 4).
	EngineWorkers int

	// SecretsDir holds secret files; empty selects <data dir>/secrets.
	SecretsDir string
	// CalendarDir confines trigger.cron calendar files; empty selects
	// <data dir>/calendars.
	CalendarDir string

	// Systemctl, Docker and Git are the binaries used for runtimes and
	// deployments. They need not exist at startup; see MissingBinaries.
	Systemctl string
	Docker    string
	Git       string
	// SystemctlPrivilege is the command prefix for system-scope systemctl
	// actions, such as "sudo -n". NoPrivilege or an empty value runs
	// systemctl directly.
	SystemctlPrivilege   string
	RuntimeStatusTimeout time.Duration
	RuntimeActionTimeout time.Duration
	// LockTTL bounds a service lock held by a crashed process. It must
	// exceed the longest lifecycle action.
	LockTTL time.Duration

	MonitorConcurrency     int
	MonitorRawRetention    time.Duration
	MonitorRollupRetention time.Duration
	// AlertServiceLabels name the alert labels that map an alert to a service.
	AlertServiceLabels []string

	TimelineRetention time.Duration
	// RunRetentionKeep and RunRetentionAge together decide which finished
	// runs are pruned: a run is kept if it is among the newest
	// RunRetentionKeep of its policy or younger than RunRetentionAge.
	RunRetentionKeep int
	RunRetentionAge  time.Duration
	// PruneInterval is how often the retention jobs run.
	PruneInterval time.Duration

	// GitHubWebhookSecret is the name (not the value) of the secret that
	// verifies GitHub push webhooks. Empty means pushes must authenticate
	// with an API token like any other ingest call.
	GitHubWebhookSecret string
}

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Listen:     DefaultListen,
		DataDir:    DefaultDataDir,
		LogFormat:  DefaultLogFormat,
		LogLevel:   DefaultLogLevel,
		SessionTTL: DefaultSessionTTL,

		Systemctl:            "systemctl",
		Docker:               "docker",
		Git:                  "git",
		SystemctlPrivilege:   DefaultSystemctlPrivilege,
		RuntimeStatusTimeout: DefaultRuntimeStatusTimeout,
		RuntimeActionTimeout: DefaultRuntimeActionTimeout,
		LockTTL:              DefaultLockTTL,

		MonitorConcurrency:     DefaultMonitorConcurrency,
		MonitorRawRetention:    DefaultMonitorRawRetention,
		MonitorRollupRetention: DefaultMonitorRollupRetention,
		AlertServiceLabels:     slices.Clone(DefaultAlertServiceLabels),

		TimelineRetention: DefaultTimelineRetention,
		RunRetentionKeep:  DefaultRunRetentionKeep,
		RunRetentionAge:   DefaultRunRetentionAge,
		PruneInterval:     DefaultPruneInterval,
	}
}

// FromEnv builds a Config from defaults overridden by environment variables.
// It reports unparsable values but does not validate semantics; call Validate
// after flags have been applied.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Default()
	if v := getenv(EnvListen); v != "" {
		c.Listen = v
	}
	if v := getenv(EnvDataDir); v != "" {
		c.DataDir = v
	}
	if v := getenv(EnvLogFormat); v != "" {
		c.LogFormat = v
	}
	if v := getenv(EnvLogLevel); v != "" {
		c.LogLevel = v
	}
	if v := getenv(EnvCookieSecure); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid boolean %q", EnvCookieSecure, v)
		}
		c.CookieSecure = b
	}
	if v := getenv(EnvSessionTTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q", EnvSessionTTL, v)
		}
		c.SessionTTL = d
	}
	if v := getenv(EnvEngineWorkers); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid integer %q", EnvEngineWorkers, v)
		}
		c.EngineWorkers = n
	}
	if err := c.componentsFromEnv(getenv); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c *Config) componentsFromEnv(getenv func(string) string) error {
	str := func(dst *string, name string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	var errs []error
	dur := func(dst *time.Duration, name string) {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid duration %q", name, v))
				return
			}
			*dst = d
		}
	}
	num := func(dst *int, name string) {
		if v := getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid integer %q", name, v))
				return
			}
			*dst = n
		}
	}
	str(&c.SecretsDir, EnvSecretsDir)
	str(&c.CalendarDir, EnvCalendarDir)
	str(&c.Systemctl, EnvSystemctl)
	str(&c.SystemctlPrivilege, EnvSystemctlPrivilege)
	str(&c.Docker, EnvDocker)
	str(&c.Git, EnvGit)
	dur(&c.RuntimeStatusTimeout, EnvRuntimeStatusTimeout)
	dur(&c.RuntimeActionTimeout, EnvRuntimeActionTimeout)
	dur(&c.LockTTL, EnvLockTTL)
	num(&c.MonitorConcurrency, EnvMonitorConcurrency)
	dur(&c.MonitorRawRetention, EnvMonitorRawRetention)
	dur(&c.MonitorRollupRetention, EnvMonitorRollupRetention)
	if v := getenv(EnvAlertServiceLabels); v != "" {
		c.AlertServiceLabels = splitList(v)
	}
	dur(&c.TimelineRetention, EnvTimelineRetention)
	num(&c.RunRetentionKeep, EnvRunRetentionKeep)
	dur(&c.RunRetentionAge, EnvRunRetentionAge)
	dur(&c.PruneInterval, EnvPruneInterval)
	str(&c.GitHubWebhookSecret, EnvGitHubWebhookSecret)
	return errors.Join(errs...)
}

// splitList splits a comma-separated list, trimming blanks and dropping
// empty items.
func splitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// stringList is a flag.Value for a comma-separated list.
type stringList struct{ p *[]string }

func (l stringList) String() string {
	if l.p == nil {
		return ""
	}
	return strings.Join(*l.p, ",")
}

func (l stringList) Set(s string) error {
	*l.p = splitList(s)
	return nil
}

// RegisterFlags registers every setting as a flag on fs. The current field
// values (defaults plus environment) become the flag defaults.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.Listen, "listen", c.Listen, "listen address ("+EnvListen+")")
	c.RegisterDataDirFlag(fs)
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "log format: text or json ("+EnvLogFormat+")")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "log level: debug, info, warn or error ("+EnvLogLevel+")")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", c.CookieSecure, "set the Secure attribute on the session cookie ("+EnvCookieSecure+")")
	fs.DurationVar(&c.SessionTTL, "session-ttl", c.SessionTTL, "session lifetime ("+EnvSessionTTL+")")
	fs.IntVar(&c.EngineWorkers, "engine-workers", c.EngineWorkers, "policy engine worker pool size, 0 for automatic ("+EnvEngineWorkers+")")

	fs.StringVar(&c.SecretsDir, "secrets-dir", c.SecretsDir, "directory of secret files, default <data-dir>/secrets ("+EnvSecretsDir+")")
	fs.StringVar(&c.CalendarDir, "calendar-dir", c.CalendarDir, "directory of cron calendar files, default <data-dir>/calendars ("+EnvCalendarDir+")")
	fs.StringVar(&c.Systemctl, "systemctl", c.Systemctl, "systemctl binary ("+EnvSystemctl+")")
	fs.StringVar(&c.SystemctlPrivilege, "systemctl-privilege", c.SystemctlPrivilege,
		"command prefix for system-scope systemctl actions; "+NoPrivilege+" or empty for none ("+EnvSystemctlPrivilege+")")
	fs.StringVar(&c.Docker, "docker", c.Docker, "docker binary ("+EnvDocker+")")
	fs.StringVar(&c.Git, "git", c.Git, "git binary ("+EnvGit+")")
	fs.DurationVar(&c.RuntimeStatusTimeout, "runtime-status-timeout", c.RuntimeStatusTimeout, "timeout of runtime status queries ("+EnvRuntimeStatusTimeout+")")
	fs.DurationVar(&c.RuntimeActionTimeout, "runtime-action-timeout", c.RuntimeActionTimeout, "timeout of start, stop and restart ("+EnvRuntimeActionTimeout+")")
	fs.DurationVar(&c.LockTTL, "lock-ttl", c.LockTTL, "expiry of a service lock whose holder crashed ("+EnvLockTTL+")")
	fs.IntVar(&c.MonitorConcurrency, "monitor-concurrency", c.MonitorConcurrency, "simultaneously running health probes ("+EnvMonitorConcurrency+")")
	fs.DurationVar(&c.MonitorRawRetention, "monitor-raw-retention", c.MonitorRawRetention, "how long raw check results are kept ("+EnvMonitorRawRetention+")")
	fs.DurationVar(&c.MonitorRollupRetention, "monitor-rollup-retention", c.MonitorRollupRetention, "how long one-minute rollups are kept ("+EnvMonitorRollupRetention+")")
	fs.Var(stringList{&c.AlertServiceLabels}, "alert-service-labels", "comma-separated alert labels that name the service ("+EnvAlertServiceLabels+")")
	fs.DurationVar(&c.TimelineRetention, "timeline-retention", c.TimelineRetention, "how long timeline entries are kept ("+EnvTimelineRetention+")")
	fs.IntVar(&c.RunRetentionKeep, "run-retention-keep", c.RunRetentionKeep, "finished runs kept per policy regardless of age ("+EnvRunRetentionKeep+")")
	fs.DurationVar(&c.RunRetentionAge, "run-retention-age", c.RunRetentionAge, "finished runs younger than this are kept ("+EnvRunRetentionAge+")")
	fs.DurationVar(&c.PruneInterval, "prune-interval", c.PruneInterval, "how often retention jobs run ("+EnvPruneInterval+")")
	fs.StringVar(&c.GitHubWebhookSecret, "github-webhook-secret", c.GitHubWebhookSecret,
		"name of the secret that verifies GitHub push webhooks ("+EnvGitHubWebhookSecret+")")
}

// RegisterDataDirFlag registers only the data directory flag, for commands
// that open the database without serving.
func (c *Config) RegisterDataDirFlag(fs *flag.FlagSet) {
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data directory ("+EnvDataDir+")")
}

// Validate checks the values without touching the filesystem.
func (c Config) Validate() error {
	var errs []error
	if _, err := ParseLevel(c.LogLevel); err != nil {
		errs = append(errs, err)
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log format %q: must be text or json", c.LogFormat))
	}
	if _, _, err := splitListen(c.Listen); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		errs = append(errs, errors.New("data dir must not be empty"))
	}
	if c.SessionTTL <= 0 {
		errs = append(errs, fmt.Errorf("session ttl %s: must be positive", c.SessionTTL))
	}
	if c.EngineWorkers < 0 || c.EngineWorkers > MaxEngineWorkers {
		errs = append(errs, fmt.Errorf("engine workers %d: must be between 0 and %d", c.EngineWorkers, MaxEngineWorkers))
	}
	errs = append(errs, c.validateComponents()...)
	return errors.Join(errs...)
}

func (c Config) validateComponents() []error {
	var errs []error
	positive := func(name string, d time.Duration) {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s %s: must be positive", name, d))
		}
	}
	for name, bin := range map[string]string{"systemctl": c.Systemctl, "docker": c.Docker, "git": c.Git} {
		if strings.TrimSpace(bin) == "" {
			errs = append(errs, fmt.Errorf("%s binary must not be empty", name))
		}
	}
	positive("runtime status timeout", c.RuntimeStatusTimeout)
	positive("runtime action timeout", c.RuntimeActionTimeout)
	positive("lock ttl", c.LockTTL)
	if c.LockTTL > lock.MaxTTL {
		errs = append(errs, fmt.Errorf("lock ttl %s: must not exceed %s", c.LockTTL, lock.MaxTTL))
	}
	if c.MonitorConcurrency < 1 || c.MonitorConcurrency > MaxMonitorConcurrency {
		errs = append(errs, fmt.Errorf("monitor concurrency %d: must be between 1 and %d", c.MonitorConcurrency, MaxMonitorConcurrency))
	}
	positive("monitor raw retention", c.MonitorRawRetention)
	positive("monitor rollup retention", c.MonitorRollupRetention)
	if c.MonitorRollupRetention < c.MonitorRawRetention {
		errs = append(errs, fmt.Errorf("monitor rollup retention %s: must not be shorter than raw retention %s",
			c.MonitorRollupRetention, c.MonitorRawRetention))
	}
	if len(c.AlertServiceLabels) == 0 {
		errs = append(errs, errors.New("alert service labels must not be empty"))
	}
	positive("timeline retention", c.TimelineRetention)
	if c.RunRetentionKeep < 1 {
		errs = append(errs, fmt.Errorf("run retention keep %d: must be at least 1", c.RunRetentionKeep))
	}
	positive("run retention age", c.RunRetentionAge)
	positive("prune interval", c.PruneInterval)
	if c.GitHubWebhookSecret != "" && !secret.ValidName(c.GitHubWebhookSecret) {
		errs = append(errs, fmt.Errorf("github webhook secret %q: not a valid secret name", c.GitHubWebhookSecret))
	}
	return errs
}

// Privilege returns the systemctl privilege prefix as an argv list. A
// non-nil empty list means "run without a prefix", which is how the
// runtime package distinguishes it from "use its default".
func (c Config) Privilege() []string {
	fields := strings.Fields(c.SystemctlPrivilege)
	if len(fields) == 0 || (len(fields) == 1 && strings.EqualFold(fields[0], NoPrivilege)) {
		return []string{}
	}
	return fields
}

// SecretsPath returns the effective secrets directory.
func (c Config) SecretsPath() string {
	if c.SecretsDir != "" {
		return c.SecretsDir
	}
	return filepath.Join(c.DataDir, SecretsDirName)
}

// CalendarPath returns the effective calendar directory.
func (c Config) CalendarPath() string {
	if c.CalendarDir != "" {
		return c.CalendarDir
	}
	return filepath.Join(c.DataDir, CalendarDirName)
}

// SourcesPath returns the directory of config source clones. It is always
// inside the data directory: clones are LCM's own state and must never
// share a directory with a user's working copy.
func (c Config) SourcesPath() string {
	return filepath.Join(c.DataDir, SourcesDirName)
}

// EnsureServeDirs creates the secrets, calendar and config source
// directories (mode 0700) when missing.
func (c Config) EnsureServeDirs() error {
	dirs := map[string]string{"secrets dir": c.SecretsPath(), "calendar dir": c.CalendarPath(), "sources dir": c.SourcesPath()}
	for name, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("%s %q: %w", name, dir, err)
		}
	}
	return nil
}

// MissingBinaries lists the configured binaries that cannot be found, as
// "name (path)". Missing binaries are not errors because a host may use
// only some runtimes; callers should warn.
func (c Config) MissingBinaries() []string {
	var missing []string
	for _, b := range []struct{ name, path string }{
		{"systemctl", c.Systemctl}, {"docker", c.Docker}, {"git", c.Git},
	} {
		if _, err := exec.LookPath(b.path); err != nil {
			missing = append(missing, b.name+" ("+b.path+")")
		}
	}
	return missing
}

// EnsureDataDir creates the data directory (mode 0700) when missing and
// verifies that it is writable.
func (c Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir %q: %w", c.DataDir, err)
	}
	f, err := os.CreateTemp(c.DataDir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("data dir %q is not writable: %w", c.DataDir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("data dir %q: %w", c.DataDir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("data dir %q: %w", c.DataDir, err)
	}
	return nil
}

// DBPath returns the SQLite file path.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, DBFileName)
}

// IsLoopback reports whether the listen address only accepts local clients.
func (c Config) IsLoopback() bool {
	host, _, err := splitListen(c.Listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ParseLevel converts a level name to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("log level %q: must be debug, info, warn or error", s)
}

// NewLogger builds the process logger writing to w. The configuration must
// have been validated.
func (c Config) NewLogger(w io.Writer) *slog.Logger {
	level, err := ParseLevel(c.LogLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if c.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func splitListen(addr string) (host string, port int, err error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("listen address %q: %w", addr, err)
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("listen address %q: invalid port %q", addr, p)
	}
	return host, port, nil
}
