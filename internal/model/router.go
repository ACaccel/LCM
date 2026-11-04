package model

import "time"

// RouterConfig is the config of a router resource: how often LCM observes it
// and when its native health turns degraded. Address and credentials live in
// the host's connection configuration, never in the document.
type RouterConfig struct {
	Interval   Duration         `yaml:"interval,omitempty" json:"interval"`
	StaleAfter Duration         `yaml:"staleAfter,omitempty" json:"staleAfter"`
	Thresholds RouterThresholds `yaml:"thresholds,omitempty" json:"thresholds"`
}

func (*RouterConfig) resourceConfig() {}

// RouterThresholds requires consecutive fresh samples before an alert transition.
type RouterThresholds struct {
	CPUPercent    float64 `yaml:"cpuPercent,omitempty" json:"cpuPercent"`
	MemoryPercent float64 `yaml:"memoryPercent,omitempty" json:"memoryPercent"`
	Consecutive   int     `yaml:"consecutive,omitempty" json:"consecutive"`
	Hysteresis    float64 `yaml:"hysteresis,omitempty" json:"hysteresis"`
}

func (c *RouterConfig) normalize(string) {
	if c.Interval == 0 {
		c.Interval = Duration(60 * time.Second)
	}
	if c.StaleAfter == 0 {
		c.StaleAfter = c.Interval * 3
	}
	t := &c.Thresholds
	if t.CPUPercent == 0 {
		t.CPUPercent = 90
	}
	if t.MemoryPercent == 0 {
		t.MemoryPercent = 90
	}
	if t.Consecutive == 0 {
		t.Consecutive = 3
	}
	if t.Hysteresis == 0 {
		t.Hysteresis = 5
	}
}

func (c *RouterConfig) validate(_ string, base string, is *Issues) {
	// Routers answer slowly and rate-limit their admin interface, so the
	// interval floor is higher than for the local machine.
	validateInterval(c.Interval, c.StaleAfter, 15*time.Second, base, is)
	t := c.Thresholds
	validatePercents(map[string]float64{"cpuPercent": t.CPUPercent, "memoryPercent": t.MemoryPercent}, base+".thresholds", is)
	validateTransition(t.Consecutive, t.Hysteresis, min(t.CPUPercent, t.MemoryPercent), base+".thresholds", is)
}
