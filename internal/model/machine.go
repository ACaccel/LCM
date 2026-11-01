package model

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"
)

// KindMachine identifies a managed physical machine document.
const KindMachine Kind = "Machine"

// Machine configures observation of the local controller host.
type Machine struct {
	Header `yaml:",inline"`
	Spec   MachineSpec `yaml:"spec" json:"spec"`
}

func (m *Machine) DocKind() Kind   { return KindMachine }
func (m *Machine) DocName() string { return m.Metadata.Name }

// MachineSpec contains desired configuration, never observed state or privileges.
type MachineSpec struct {
	Host       string            `yaml:"host,omitempty" json:"host"`
	Monitoring MachineMonitoring `yaml:"monitoring,omitempty" json:"monitoring"`
}

// MachineMonitoring bounds the local data sources and their health evaluation.
type MachineMonitoring struct {
	Interval    Duration          `yaml:"interval,omitempty" json:"interval"`
	StaleAfter  Duration          `yaml:"staleAfter,omitempty" json:"staleAfter"`
	Filesystems []string          `yaml:"filesystems,omitempty" json:"filesystems"`
	Interfaces  []string          `yaml:"interfaces,omitempty" json:"interfaces,omitempty"`
	Units       []string          `yaml:"units,omitempty" json:"units,omitempty"`
	Journal     bool              `yaml:"journal,omitempty" json:"journal"`
	Thresholds  MachineThresholds `yaml:"thresholds,omitempty" json:"thresholds"`
}

// MachineThresholds requires consecutive fresh samples before an alert transition.
type MachineThresholds struct {
	CPUPercent    float64 `yaml:"cpuPercent,omitempty" json:"cpuPercent"`
	MemoryPercent float64 `yaml:"memoryPercent,omitempty" json:"memoryPercent"`
	DiskPercent   float64 `yaml:"diskPercent,omitempty" json:"diskPercent"`
	Consecutive   int     `yaml:"consecutive,omitempty" json:"consecutive"`
	Hysteresis    float64 `yaml:"hysteresis,omitempty" json:"hysteresis"`
}

func (m *Machine) normalize() {
	if m.Spec.Host == "" {
		m.Spec.Host = HostLocal
	}
	o := &m.Spec.Monitoring
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

// ValidateMachine rejects unsupported connections and unbounded source selection.
func ValidateMachine(m *Machine) error {
	var is Issues
	validateHeader(&m.Header, KindMachine, &is)
	if m.Spec.Host != HostLocal {
		is.addf("spec.host", "only local is supported")
	}
	o := m.Spec.Monitoring
	if o.Interval.D() < 5*time.Second || o.Interval.D() > time.Hour {
		is.addf("spec.monitoring.interval", "must be between 5s and 1h")
	}
	if o.StaleAfter < 2*o.Interval || o.StaleAfter.D() > 24*time.Hour {
		is.addf("spec.monitoring.staleAfter", "must be at least twice interval and at most 24h")
	}
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
	validateList(o.Filesystems, "spec.monitoring.filesystems", func(v string) bool {
		return len(v) <= 4096 && path.IsAbs(v) && path.Clean(v) == v && !strings.ContainsAny(v, "\x00\r\n")
	})
	validateList(o.Interfaces, "spec.monitoring.interfaces", machineInterfaceRe.MatchString)
	validateList(o.Units, "spec.monitoring.units", func(v string) bool { return len(v) <= 255 && unitRe.MatchString(v) })
	if o.Journal && len(o.Units) == 0 {
		is.addf("spec.monitoring.units", "journal requires an explicit unit allowlist")
	}
	t := o.Thresholds
	for key, v := range map[string]float64{"cpuPercent": t.CPUPercent, "memoryPercent": t.MemoryPercent, "diskPercent": t.DiskPercent} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 100 {
			is.addf("spec.monitoring.thresholds."+key, "must be greater than 0 and at most 100")
		}
	}
	if t.Consecutive < 1 || t.Consecutive > 120 {
		is.addf("spec.monitoring.thresholds.consecutive", "must be between 1 and 120")
	}
	if math.IsNaN(t.Hysteresis) || math.IsInf(t.Hysteresis, 0) || t.Hysteresis <= 0 || t.Hysteresis >= min(t.CPUPercent, t.MemoryPercent, t.DiskPercent) {
		is.addf("spec.monitoring.thresholds.hysteresis", "must be positive and below every threshold")
	}
	return is.Err()
}

// DecodeMachine decodes and requires a Machine document.
func DecodeMachine(data []byte) (*Machine, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	m, ok := doc.(*Machine)
	if !ok {
		return nil, Issues{{Path: "kind", Message: "must be Machine"}}
	}
	return m, nil
}
