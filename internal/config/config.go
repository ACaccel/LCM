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
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
)

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
}

// FromEnv builds a Config from defaults overridden by environment variables.
// It reports unparsable values but does not validate semantics; call Validate
// after flags have been applied.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:     DefaultListen,
		DataDir:    DefaultDataDir,
		LogFormat:  DefaultLogFormat,
		LogLevel:   DefaultLogLevel,
		SessionTTL: DefaultSessionTTL,
	}
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
	return c, nil
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
	return errors.Join(errs...)
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
