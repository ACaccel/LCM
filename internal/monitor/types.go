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

// Health is the combined state of a resource.
type Health string

// Health values, from best to worst: healthy, unknown, degraded, down.
const (
	HealthUnknown  Health = "unknown"
	HealthHealthy  Health = "healthy"
	HealthDegraded Health = "degraded"
	HealthDown     Health = "down"
)

// Def is a check together with the resource it belongs to. Sync takes the
// complete list of definitions.
type Def struct {
	Resource string
	// ResourceType is the type of Resource, such as "service"; signals
	// carry it.
	ResourceType string
	Labels       map[string]string
	Check        model.Check
	// Paused keeps the check without running it and leaves it out of the
	// resource's health, such as a check of an instance an operator
	// stopped; PauseReason says why.
	Paused      bool
	PauseReason string
}

// ID returns the check's identity, "resource/check".
func (d Def) ID() string { return checkID(d.Resource, d.Check.Name) }

func checkID(resource, check string) string { return resource + "/" + check }

// CheckStatus is the current state of one check.
type CheckStatus struct {
	Resource string        `json:"resource"`
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
	// Message is the latest result's message: an error text when failing;
	// for a paused check, why it is paused.
	Message             string        `json:"message,omitempty"`
	LastResult          time.Time     `json:"lastResult"`
	LastLatency         time.Duration `json:"lastLatency"`
	ConsecutiveFailures int           `json:"consecutiveFailures"`
	// Instance is the instance of a managed service the check belongs to.
	Instance string `json:"instance,omitempty"`
	// Paused checks do not run and do not count towards health; Status is
	// unknown while paused.
	Paused bool `json:"paused,omitempty"`
}

// ResourceStatus is the current state of one resource: its combined health,
// its checks and the native health its type reported.
type ResourceStatus struct {
	Resource string            `json:"resource"`
	Type     string            `json:"type,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	// Health is the worst of the checks' health and the native health.
	Health Health        `json:"health"`
	Since  time.Time     `json:"since"`
	Checks []CheckStatus `json:"checks"`
	// Native is the health the resource type evaluated itself (machine or
	// router thresholds and reachability); nil when it reports none.
	Native *NativeStatus `json:"native,omitempty"`
}

// NativeStatus is the type-evaluated health of a resource, see SetNative.
type NativeStatus struct {
	Health  Health    `json:"health"`
	Reasons []string  `json:"reasons,omitempty"`
	Since   time.Time `json:"since"`
}
