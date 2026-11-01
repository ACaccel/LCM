package observe

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxUnits      = 64
	maxPaths      = 32
	maxInterfaces = 32
	maxOutput     = 4 << 20
	maxFile       = 1 << 20
)

var (
	unitPattern      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@-]*\.(service|socket|target|mount|automount|swap|timer|path|slice|scope)$`)
	interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]*$`)
	tokenPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Config contains deployment-owned allowlists, never machine document privileges.
// Empty lists disable those sources; no filesystem, interface, or unit discovery
// is performed. Zero command limits select bounded defaults.
type Config struct {
	Filesystems    []string
	Interfaces     []string
	Units          []string
	PSI            bool
	CommandTimeout time.Duration
	MaxOutput      int
}

func (c Config) normalized() (Config, error) {
	if err := validateList(c.Filesystems, maxPaths, func(s string) bool {
		return len(s) <= 4096 && filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
	}); err != nil {
		return c, fmt.Errorf("filesystems: %w", err)
	}
	if err := validateList(c.Interfaces, maxInterfaces, func(s string) bool {
		return len(s) <= 15 && interfacePattern.MatchString(s)
	}); err != nil {
		return c, fmt.Errorf("interfaces: %w", err)
	}
	if err := validateUnits(c.Units); err != nil {
		return c, err
	}
	var err error
	c.CommandTimeout, c.MaxOutput, err = commandLimits(c.CommandTimeout, c.MaxOutput)
	c.Filesystems = append([]string(nil), c.Filesystems...)
	c.Interfaces = append([]string(nil), c.Interfaces...)
	c.Units = append([]string(nil), c.Units...)
	return c, err
}

func commandLimits(timeout time.Duration, output int) (time.Duration, int, error) {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	if output == 0 {
		output = 1 << 20
	}
	if timeout < 100*time.Millisecond || timeout > 30*time.Second {
		return 0, 0, errors.New("command timeout must be between 100ms and 30s")
	}
	if output < 1024 || output > maxOutput {
		return 0, 0, errors.New("output limit must be between 1KiB and 4MiB")
	}
	return timeout, output, nil
}

func validUnit(s string) bool { return len(s) <= 255 && unitPattern.MatchString(s) }

func validateUnits(units []string) error {
	if err := validateList(units, maxUnits, validUnit); err != nil {
		return fmt.Errorf("units: %w", err)
	}
	return nil
}

func validateList(values []string, limit int, valid func(string) bool) error {
	if len(values) > limit {
		return fmt.Errorf("allowlist exceeds %d entries", limit)
	}
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if !valid(v) {
			return errors.New("invalid allowlist entry")
		}
		if seen[v] {
			return errors.New("duplicate allowlist entry")
		}
		seen[v] = true
	}
	return nil
}
