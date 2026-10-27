package alert

import (
	"encoding/json"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

type gatusPayload struct {
	Endpoint    string            `json:"endpoint"`
	Group       string            `json:"group"`
	URL         string            `json:"url"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	Service     string            `json:"service"`
	Severity    string            `json:"severity"`
	Message     string            `json:"message"`
	Labels      map[string]string `json:"labels"`
}

func parseGatus(body []byte, opts Options) ([]signal.Signal, error) {
	var p gatusPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, invalid(SourceGatus, err)
	}
	if p.Endpoint == "" {
		return nil, invalidf(SourceGatus, "endpoint is required")
	}
	var status string
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "triggered", StatusFiring:
		status = StatusFiring
	case "resolved":
		status = StatusResolved
	default:
		return nil, invalidf(SourceGatus, "status must be TRIGGERED or RESOLVED")
	}
	labels := map[string]string{"endpoint": p.Endpoint}
	if p.Group != "" {
		labels["group"] = p.Group
	}
	for k, v := range p.Labels {
		labels[k] = v
	}
	service := strings.TrimSpace(p.Service)
	if service == "" {
		service = opts.serviceOf(labels)
	}
	sev := normalizeSeverity(p.Severity, signal.SeverityWarning)
	if status == StatusResolved {
		sev = signal.SeverityInfo
	}
	msg := p.Message
	if msg == "" {
		msg = p.Description
	}
	return []signal.Signal{build(SourceGatus, service, p.Endpoint, status, sev, msg,
		fingerprint(p.Group, p.Endpoint), labels, map[string]any{"url": p.URL})}, nil
}
