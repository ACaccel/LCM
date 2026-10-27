package model

import (
	"fmt"
	"time"
)

// Check defaults and limits.
const (
	DefaultCheckInterval  = Duration(30 * time.Second)
	DefaultCheckTimeout   = Duration(5 * time.Second)
	MinCheckInterval      = Duration(time.Second)
	DefaultCheckThreshold = 1
	MaxCheckThreshold     = 100
)

// Check is a health check attached to a service. Type-specific settings live
// in Props and are decoded and validated by the monitor's check registry, the
// same way node props are handled by the node registry.
type Check struct {
	Name string `yaml:"name" json:"name"`
	Type string `yaml:"type" json:"type"`
	// Interval between runs; defaults to DefaultCheckInterval.
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	// Timeout of one run; defaults to DefaultCheckTimeout and must be
	// shorter than Interval.
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// Critical checks make the service down when failing; non-critical
	// ones make it degraded. Nil means critical.
	Critical *bool `yaml:"critical,omitempty" json:"critical,omitempty"`
	// Threshold is the number of consecutive failures (and successes)
	// needed before the check changes status; defaults to 1.
	Threshold int            `yaml:"threshold,omitempty" json:"threshold,omitempty"`
	Props     map[string]any `yaml:"props,omitempty" json:"props,omitempty"`
}

// IsCritical reports whether a failing check makes its service down.
func (c Check) IsCritical() bool { return c.Critical == nil || *c.Critical }

// WithDefaults returns a copy with unset fields filled in.
func (c Check) WithDefaults() Check {
	if c.Interval == 0 {
		c.Interval = DefaultCheckInterval
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultCheckTimeout
	}
	if c.Threshold == 0 {
		c.Threshold = DefaultCheckThreshold
	}
	return c
}

// ValidateChecks checks the structure of a service's checks; path is the
// location of the list, such as "spec.checks". Type-specific props are not
// inspected.
func ValidateChecks(checks []Check, path string) error {
	var is Issues
	validateChecks(checks, path, &is)
	return is.Err()
}

func validateChecks(checks []Check, path string, is *Issues) {
	seen := map[string]bool{}
	for i, c := range checks {
		p := fmt.Sprintf("%s[%d]", path, i)
		switch {
		case !nameRe.MatchString(c.Name) || len(c.Name) > maxNameLen:
			is.addf(p+".name", "must be a lowercase name of letters, digits and dashes (max %d)", maxNameLen)
		case seen[c.Name]:
			is.addf(p+".name", "duplicate check name %q", c.Name)
		}
		seen[c.Name] = true
		if !typeRe.MatchString(c.Type) {
			is.addf(p+".type", "invalid check type %q", c.Type)
		}
		d := c.WithDefaults()
		if d.Interval < MinCheckInterval {
			is.addf(p+".interval", "must be at least %s", MinCheckInterval.D())
		}
		if d.Timeout <= 0 || d.Timeout >= d.Interval {
			is.addf(p+".timeout", "must be positive and shorter than the interval")
		}
		if c.Threshold < 0 || c.Threshold > MaxCheckThreshold {
			is.addf(p+".threshold", "must be between 1 and %d", MaxCheckThreshold)
		}
	}
}
