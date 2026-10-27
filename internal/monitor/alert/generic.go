package alert

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

type genericPayload struct {
	Service     string            `json:"service"`
	Alert       string            `json:"alert"`
	Status      string            `json:"status"`
	Severity    string            `json:"severity"`
	Labels      map[string]string `json:"labels"`
	Message     string            `json:"message"`
	Fingerprint string            `json:"fingerprint"`
}

func parseGeneric(body []byte, opts Options) ([]signal.Signal, error) {
	var items []genericPayload
	if bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("[")) {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, invalid(SourceGeneric, err)
		}
	} else {
		var one genericPayload
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, invalid(SourceGeneric, err)
		}
		items = []genericPayload{one}
	}
	out := make([]signal.Signal, 0, len(items))
	for i, p := range items {
		if strings.TrimSpace(p.Alert) == "" {
			return nil, invalidf(SourceGeneric, "item %d: alert is required", i)
		}
		status := strings.ToLower(strings.TrimSpace(p.Status))
		if status != StatusFiring && status != StatusResolved {
			return nil, invalidf(SourceGeneric, "item %d: status must be firing or resolved", i)
		}
		service := strings.TrimSpace(p.Service)
		if service == "" {
			service = opts.serviceOf(p.Labels)
		}
		sev := normalizeSeverity(p.Severity, signal.SeverityWarning)
		if status == StatusResolved {
			sev = signal.SeverityInfo
		}
		fp := p.Fingerprint
		if fp == "" {
			fp = fingerprint(service, p.Alert, labelFingerprint(p.Labels))
		}
		out = append(out, build(SourceGeneric, service, p.Alert, status, sev, p.Message, fp, p.Labels, nil))
	}
	return out, nil
}
