package monitor

import (
	"github.com/ACaccel/lcm/internal/model"
)

type heartbeatProps struct {
	// Grace is extra time beyond the interval before a missing heartbeat
	// counts as a failure; the default is one interval.
	Grace model.Duration `json:"grace"`
}

func buildHeartbeat(c model.Check, p heartbeatProps, _ Env) (Built, error) {
	if p.Grace < 0 {
		return Built{}, propError("grace", "must not be negative")
	}
	grace := p.Grace.D()
	if grace == 0 {
		grace = c.Interval.D()
	}
	return Built{Grace: grace}, nil
}
