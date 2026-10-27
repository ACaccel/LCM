package alert

import (
	"encoding/json"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

type amPayload struct {
	Alerts       *[]amAlert        `json:"alerts"`
	CommonLabels map[string]string `json:"commonLabels"`
}

type amAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func parseAlertmanager(body []byte, opts Options) ([]signal.Signal, error) {
	var p amPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, invalid(SourceAlertmanager, err)
	}
	if p.Alerts == nil {
		return nil, invalidf(SourceAlertmanager, "missing alerts")
	}
	out := make([]signal.Signal, 0, len(*p.Alerts))
	for i, a := range *p.Alerts {
		status := strings.ToLower(a.Status)
		if status != StatusFiring && status != StatusResolved {
			return nil, invalidf(SourceAlertmanager, "alerts[%d]: status must be firing or resolved", i)
		}
		labels := make(map[string]string, len(p.CommonLabels)+len(a.Labels))
		for k, v := range p.CommonLabels {
			labels[k] = v
		}
		for k, v := range a.Labels {
			labels[k] = v
		}
		name := labels["alertname"]
		if name == "" {
			name = "alert"
		}
		fp := a.Fingerprint
		if fp == "" {
			fp = labelFingerprint(a.Labels)
		}
		msg := a.Annotations["summary"]
		if msg == "" {
			msg = a.Annotations["description"]
		}
		sev := normalizeSeverity(labels["severity"], signal.SeverityWarning)
		if status == StatusResolved {
			sev = signal.SeverityInfo
		}
		out = append(out, build(SourceAlertmanager, opts.serviceOf(labels), name, status, sev, msg, fp, labels, map[string]any{
			"startsAt":     a.StartsAt,
			"endsAt":       a.EndsAt,
			"generatorURL": a.GeneratorURL,
		}))
	}
	return out, nil
}
