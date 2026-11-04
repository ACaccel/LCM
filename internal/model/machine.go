package model

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"
)

// MachineConfig is the config of a machine resource: the bounded local data
// sources and their health evaluation. It holds desired configuration only,
// never observed state or privileges.
type MachineConfig struct {
	Interval    Duration          `yaml:"interval,omitempty" json:"interval"`
	StaleAfter  Duration          `yaml:"staleAfter,omitempty" json:"staleAfter"`
	Filesystems []string          `yaml:"filesystems,omitempty" json:"filesystems"`
	Interfaces  []string          `yaml:"interfaces,omitempty" json:"interfaces,omitempty"`
	Units       []string          `yaml:"units,omitempty" json:"units,omitempty"`
	Journal     bool              `yaml:"journal,omitempty" json:"journal"`
	Thresholds  MachineThresholds `yaml:"thresholds,omitempty" json:"thresholds"`
}

func (*MachineConfig) resourceConfig() {}

// MachineThresholds requires consecutive fresh samples before an alert transition.
type MachineThresholds struct {
	CPUPercent    float64 `yaml:"cpuPercent,omitempty" json:"cpuPercent"`
	MemoryPercent float64 `yaml:"memoryPercent,omitempty" json:"memoryPercent"`
	DiskPercent   float64 `yaml:"diskPercent,omitempty" json:"diskPercent"`
	Consecutive   int     `yaml:"consecutive,omitempty" json:"consecutive"`
	Hysteresis    float64 `yaml:"hysteresis,omitempty" json:"hysteresis"`
}

func (o *MachineConfig) normalize(string) {
	if o.Interval == 0 {
		o.Interval = Duration(30 * time.Second)
	}
	if o.StaleAfter == 0 {
		o.StaleAfter = o.Interval * 3
	}
	if o.Filesystems == nil {
		o.Filesystems = []string{"/"}
	}
	t := &o.Thresholds
	if t.CPUPercent == 0 {
		t.CPUPercent = 90
	}
	if t.MemoryPercent == 0 {
		t.MemoryPercent = 90
	}
	if t.DiskPercent == 0 {
		t.DiskPercent = 90
	}
	if t.Consecutive == 0 {
		t.Consecutive = 3
	}
	if t.Hysteresis == 0 {
		t.Hysteresis = 5
	}
}

var machineInterfaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,14}$`)

// validate rejects unbounded source selection and unusable thresholds.
func (o *MachineConfig) validate(_ string, base string, is *Issues) {
	validateInterval(o.Interval, o.StaleAfter, 5*time.Second, base, is)
	validateList := func(values []string, field string, valid func(string) bool) {
		if len(values) > 32 {
			is.addf(field, "at most 32 entries are allowed")
		}
		seen := map[string]bool{}
		for i, v := range values {
			if !valid(v) || seen[v] {
				is.addf(fmt.Sprintf("%s[%d]", field, i), "invalid or duplicate value")
			}
			seen[v] = true
		}
	}
	validateList(o.Filesystems, base+".filesystems", func(v string) bool {
		return len(v) <= 4096 && path.IsAbs(v) && path.Clean(v) == v && !strings.ContainsAny(v, "\x00\r\n")
	})
	validateList(o.Interfaces, base+".interfaces", machineInterfaceRe.MatchString)
	validateList(o.Units, base+".units", func(v string) bool { return len(v) <= 255 && unitRe.MatchString(v) })
	if o.Journal && len(o.Units) == 0 {
		is.addf(base+".units", "journal requires an explicit unit allowlist")
	}
	t := o.Thresholds
	validatePercents(map[string]float64{"cpuPercent": t.CPUPercent, "memoryPercent": t.MemoryPercent, "diskPercent": t.DiskPercent}, base+".thresholds", is)
	validateTransition(t.Consecutive, t.Hysteresis, min(t.CPUPercent, t.MemoryPercent, t.DiskPercent), base+".thresholds", is)
}

// validateInterval checks an observation interval of at least minimum and at
// most one hour, and a staleness bound of two intervals up to one day.
func validateInterval(interval, staleAfter Duration, minimum time.Duration, base string, is *Issues) {
	if interval.D() < minimum || interval.D() > time.Hour {
		is.addf(base+".interval", "must be between %s and 1h", minimum)
	}
	if staleAfter < 2*interval || staleAfter.D() > 24*time.Hour {
		is.addf(base+".staleAfter", "must be at least twice interval and at most 24h")
	}
}

func validatePercents(values map[string]float64, base string, is *Issues) {
	for _, key := range sortedKeys(values) {
		if v := values[key]; math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 100 {
			is.addf(base+"."+key, "must be greater than 0 and at most 100")
		}
	}
}

func validateTransition(consecutive int, hysteresis, lowest float64, base string, is *Issues) {
	if consecutive < 1 || consecutive > 120 {
		is.addf(base+".consecutive", "must be between 1 and 120")
	}
	if math.IsNaN(hysteresis) || math.IsInf(hysteresis, 0) || hysteresis <= 0 || hysteresis >= lowest {
		is.addf(base+".hysteresis", "must be positive and below every threshold")
	}
}
