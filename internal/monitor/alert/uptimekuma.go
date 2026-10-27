package alert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

// Uptime Kuma heartbeat states.
const (
	kumaDown = 0
	kumaUp   = 1
)

type kumaPayload struct {
	Msg       string         `json:"msg"`
	Heartbeat *kumaHeartbeat `json:"heartbeat"`
	Monitor   *kumaMonitor   `json:"monitor"`
}

type kumaHeartbeat struct {
	Status    int    `json:"status"`
	Time      string `json:"time"`
	Msg       string `json:"msg"`
	Important bool   `json:"important"`
}

type kumaMonitor struct {
	ID       json.Number `json:"id"`
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	URL      string      `json:"url"`
	Hostname string      `json:"hostname"`
	Tags     []kumaTag   `json:"tags"`
}

type kumaTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func parseUptimeKuma(body []byte, opts Options) ([]signal.Signal, error) {
	var p kumaPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, invalid(SourceUptimeKuma, err)
	}
	// The "Test" button of the notification sends no heartbeat or monitor.
	if p.Heartbeat == nil || p.Monitor == nil {
		return nil, nil
	}
	var status string
	switch p.Heartbeat.Status {
	case kumaDown:
		status = StatusFiring
	case kumaUp:
		status = StatusResolved
	default:
		// Pending and maintenance are not alert transitions.
		return nil, nil
	}
	m := p.Monitor
	name := m.Name
	if name == "" {
		name = "monitor " + m.ID.String()
	}
	labels := map[string]string{"monitor": name}
	if m.Type != "" {
		labels["type"] = m.Type
	}
	for _, t := range m.Tags {
		if t.Name != "" {
			labels[t.Name] = t.Value
		}
	}
	id := m.ID.String()
	if id == "" {
		id = name
	}
	sev := signal.SeverityCritical
	if status == StatusResolved {
		sev = signal.SeverityInfo
	}
	msg := p.Heartbeat.Msg
	if msg == "" {
		msg = p.Msg
	}
	return []signal.Signal{build(SourceUptimeKuma, opts.serviceOf(labels), name, status, sev, msg,
		fmt.Sprintf("monitor-%s", strings.TrimSpace(id)), labels, map[string]any{
			"time": p.Heartbeat.Time,
			"url":  m.URL,
		})}, nil
}
