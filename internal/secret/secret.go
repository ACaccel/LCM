// Package secret resolves named secrets for nodes and triggers. Policies
// carry only secret names; values are read at the moment of use from files
// in a secrets directory or from environment variables, and are never part
// of props, traces, logs or error messages.
package secret

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// EnvPrefix is the prefix of environment variables that hold secrets: the
// secret "github-token" is read from LCM_SECRET_GITHUB_TOKEN.
const EnvPrefix = "LCM_SECRET_"

// maxSize bounds a secret file; real secrets are far smaller, and the limit
// keeps a mistakenly named large file from being slurped into memory.
const maxSize = 64 << 10

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Errors returned by Resolver.Get.
var (
	ErrInvalidName = errors.New("secret: invalid name")
	ErrNotFound    = errors.New("secret: not found")
)

// ValidName reports whether name can be used as a secret name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Value is a resolved secret. It prints as a placeholder in every format
// so that logging or marshaling it by accident does not leak it; use Reveal
// at the single place the value is sent.
type Value struct{ s string }

// NewValue wraps s.
func NewValue(s string) Value { return Value{s: s} }

// Reveal returns the secret text.
func (v Value) Reveal() string { return v.s }

// String implements fmt.Stringer.
func (Value) String() string { return "[redacted]" }

// GoString implements fmt.GoStringer.
func (Value) GoString() string { return "secret.Value{[redacted]}" }

// MarshalJSON implements json.Marshaler.
func (Value) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// LogValue implements slog.LogValuer.
func (Value) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// Source resolves secrets by name. *Resolver implements it; tests use
// small fakes.
type Source interface {
	Get(name string) (Value, error)
}

// Resolver reads secrets from files and the environment.
type Resolver struct {
	dir    string
	strict bool
	getenv func(string) (string, bool)
	log    *slog.Logger

	mu     sync.Mutex
	warned map[string]bool
}

// Option customizes a Resolver.
type Option func(*Resolver)

// WithStrict makes files that are readable by group or others an error
// instead of a warning.
func WithStrict() Option { return func(r *Resolver) { r.strict = true } }

// WithLookupEnv replaces os.LookupEnv, for tests.
func WithLookupEnv(f func(string) (string, bool)) Option {
	return func(r *Resolver) { r.getenv = f }
}

// WithLogger sets the logger used for permission warnings.
func WithLogger(l *slog.Logger) Option { return func(r *Resolver) { r.log = l } }

// New returns a Resolver. dir is the secrets directory ($LCM_SECRETS_DIR)
// and may be empty to use the environment only. Files take precedence over
// environment variables.
func New(dir string, opts ...Option) *Resolver {
	r := &Resolver{dir: dir, getenv: os.LookupEnv, log: slog.New(slog.DiscardHandler), warned: map[string]bool{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// EnvName returns the environment variable that holds the named secret.
func EnvName(name string) string {
	var b strings.Builder
	b.WriteString(EnvPrefix)
	for _, c := range strings.ToUpper(name) {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Get resolves a secret. Errors never contain the value.
func (r *Resolver) Get(name string) (Value, error) {
	if !ValidName(name) {
		return Value{}, fmt.Errorf("%w %q", ErrInvalidName, name)
	}
	if r.dir != "" {
		v, found, err := r.fromFile(name)
		if err != nil {
			return Value{}, err
		}
		if found {
			return v, nil
		}
	}
	if s, ok := r.getenv(EnvName(name)); ok && s != "" {
		return Value{s: s}, nil
	}
	return Value{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

func (r *Resolver) fromFile(name string) (Value, bool, error) {
	path := filepath.Join(r.dir, name)
	f, err := os.Open(path) //nolint:gosec // name is validated to contain no path separators
	if errors.Is(err, os.ErrNotExist) {
		return Value{}, false, nil
	}
	if err != nil {
		return Value{}, false, fmt.Errorf("secret: open %q: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Value{}, false, fmt.Errorf("secret: stat %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return Value{}, false, fmt.Errorf("secret: %q is not a regular file", name)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		if r.strict {
			return Value{}, false, fmt.Errorf("secret: %q has mode %04o; want 0600", name, mode)
		}
		r.warnOnce(name, mode)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return Value{}, false, fmt.Errorf("secret: read %q: %w", name, err)
	}
	if len(b) > maxSize {
		return Value{}, false, fmt.Errorf("secret: %q is larger than %d bytes", name, maxSize)
	}
	// Editors and `echo` append a newline that is not part of the secret.
	s := strings.TrimRight(string(b), "\r\n")
	if s == "" {
		return Value{}, false, fmt.Errorf("secret: %q is empty", name)
	}
	return Value{s: s}, true, nil
}

func (r *Resolver) warnOnce(name string, mode os.FileMode) {
	key := fmt.Sprintf("%s:%04o", name, mode)
	r.mu.Lock()
	seen := r.warned[key]
	r.warned[key] = true
	r.mu.Unlock()
	if !seen {
		r.log.Warn("secret file is accessible by other users; restrict it with chmod 600", "secret", name, "mode", fmt.Sprintf("%04o", mode))
	}
}
