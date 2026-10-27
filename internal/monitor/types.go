package monitor

import (
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

// Status is the state of one check.
type Status string

// Check statuses.
const (
	StatusUnknown Status = "unknown"
	StatusUp      Status = "up"
	StatusDown    Status = "down"
)

// Health is the aggregated state of a service.
type Health string

// Service health values.
const (
	HealthUnknown  Health = "unknown"
	HealthHealthy  Health = "healthy"
	HealthDegraded Health = "degraded"
	HealthDown     Health = "down"
)

// Def is a check together with the service it belongs to. Sync takes the
// complete list of definitions.
type Def struct {
	Service string
	Labels  map[string]string
	Check   model.Check
}

// ID returns the check's identity, "service/check".
func (d Def) ID() string { return checkID(d.Service, d.Check.Name) }

func checkID(service, check string) string { return service + "/" + check }

// CheckStatus is the current state of one check.
type CheckStatus struct {
	Service  string        `json:"service"`
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Critical bool          `json:"critical"`
	Interval time.Duration `json:"interval"`
	// Status is unknown while no result has been recorded, and after a
	// restart until a fresh result arrives if the stored one is older than
	// twice the interval (Stale is then true).
	Status Status    `json:"status"`
	Stale  bool      `json:"stale,omitempty"`
	Since  time.Time `json:"since"`
	// Message is the latest result's message: an error text when failing.
	Message             string        `json:"message,omitempty"`
	LastResult          time.Time     `json:"lastResult"`
	LastLatency         time.Duration `json:"lastLatency"`
	ConsecutiveFailures int           `json:"consecutiveFailures"`
}

// ServiceStatus is the current state of one service and its checks.
type ServiceStatus struct {
	Service string            `json:"service"`
	Labels  map[string]string `json:"labels,omitempty"`
	Health  Health            `json:"health"`
	Since   time.Time         `json:"since"`
	Checks  []CheckStatus     `json:"checks"`
}
