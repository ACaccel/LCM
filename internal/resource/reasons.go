package resource

import (
	"strings"

	"github.com/ACaccel/lcm/internal/message"
)

// Health reasons that machines and routers report next to their native
// health. They are stored with the observed status, so their text never
// changes; ReasonMessage gives each a code for clients. Threshold reasons
// are metric keys instead (see MetricMessage).
const (
	ReasonStale                  = "observations missing or stale"
	ReasonNotObserved            = "configuration has not been observed"
	ReasonObservationUnavailable = "required observation unavailable"
	ReasonUnreachable            = "router unreachable"
	ReasonAuthRejected           = "authentication rejected"
	ReasonNoStatus               = "router answered without usable status"
	ReasonNoConnection           = "connection is not configured on this host"
	ReasonConnectionType         = "connection type does not match the adapter"
	ReasonConnectionDenied       = "connection does not allow this resource"
)

// Fixed reasons of observation events.
const (
	ReasonBootChanged     = "boot identity changed"
	ReasonUptimeRestarted = "uptime restarted"
	// ReasonResolved is the reason of the event that records a manual
	// resolution of an operation.
	ReasonResolved = "resolved"
)

const notReportedSuffix = " not reported"

// NotReported is the health reason for a metric the observation lacks.
func NotReported(metric string) string { return metric + notReportedSuffix }

var reasonCodes = map[string]string{
	ReasonStale:                  "health.stale",
	ReasonNotObserved:            "health.notObserved",
	ReasonObservationUnavailable: "health.observationUnavailable",
	ReasonUnreachable:            "health.unreachable",
	ReasonAuthRejected:           "health.authRejected",
	ReasonNoStatus:               "health.noStatus",
	ReasonNoConnection:           "health.noConnection",
	ReasonConnectionType:         "health.connectionType",
	ReasonConnectionDenied:       "health.connectionDenied",
}

// ReasonMessage returns the message of a health reason. A metric key (such
// as cpu or disk:/var) is a threshold that fired. Text it does not know has
// no code.
func ReasonMessage(reason string) message.Message {
	if code, ok := reasonCodes[reason]; ok {
		return message.New(code, reason)
	}
	if metric, ok := strings.CutSuffix(reason, notReportedSuffix); ok {
		if m, ok := metricParams(metric); ok {
			return message.New("health.notReported", reason, m...)
		}
	}
	if m, ok := metricParams(reason); ok {
		return message.New("health.threshold", reason, m...)
	}
	return message.Text(reason)
}

// ReasonMessages returns the message of each health reason.
func ReasonMessages(reasons []string) []message.Message {
	out := make([]message.Message, len(reasons))
	for i, r := range reasons {
		out[i] = ReasonMessage(r)
	}
	return out
}

// MetricMessage returns the message naming a metric key: cpu, memory,
// disk:<path>, inode:<path> or unit:<name>.
func MetricMessage(key string) message.Message {
	if m, ok := metricParams(key); ok {
		return message.New("metric", key, m...)
	}
	return message.Text(key)
}

// metricParams splits a metric key into the metric and its target.
func metricParams(key string) ([]string, bool) {
	switch key {
	case "cpu", "memory":
		return []string{"metric", key, "target", ""}, true
	}
	metric, target, ok := strings.Cut(key, ":")
	if !ok || target == "" {
		return nil, false
	}
	switch metric {
	case "disk", "inode", "unit":
		return []string{"metric", metric, "target", target}, true
	}
	return nil, false
}

// EventReasonMessage returns the message of an observation event's reason,
// or nil when the event has none. Event kinds are "<type>.<event>", and the
// reason of each event is defined by the event regardless of the type.
func EventReasonMessage(e Event) *message.Message {
	if e.Reason == "" {
		return nil
	}
	_, event, _ := strings.Cut(e.Kind, ".")
	var m message.Message
	switch event {
	case "threshold.firing", "threshold.recovered":
		m = MetricMessage(e.Reason)
	case "health.changed":
		m = message.New("health.state", e.Reason, "health", e.Reason)
	case "reachability.changed":
		m = message.New("reachability", e.Reason, "reachability", e.Reason)
	case "boot.changed":
		switch e.Reason {
		case ReasonBootChanged:
			m = message.New("event.bootChanged", e.Reason)
		case ReasonUptimeRestarted:
			m = message.New("event.uptimeRestarted", e.Reason)
		default:
			m = message.Text(e.Reason)
		}
	case "collector.changed":
		// Routers name the source before its state; machines report the
		// journal's state alone.
		if source, state, ok := strings.Cut(e.Reason, " "); ok {
			m = message.New("coverage.sourceState", e.Reason, "source", source, "state", state)
		} else {
			m = message.New("coverage.state", e.Reason, "state", e.Reason)
		}
	case "collector.gap":
		m = message.New("coverage.reason", e.Reason, "reason", e.Reason)
	case "operation.changed":
		if e.Reason == ReasonResolved {
			m = message.New("operation.resolved", e.Reason)
		} else {
			m = message.New("operation.state", e.Reason, "state", e.Reason)
		}
	default:
		m = message.Text(e.Reason)
	}
	return &m
}
