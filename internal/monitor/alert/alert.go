// Package alert turns webhook payloads of external monitoring systems into
// alert signals. Parsers are pure: they do not read the clock, touch storage
// or publish anything, so they can be tested against recorded payloads.
//
// Supported sources:
//
//   - alertmanager: Prometheus Alertmanager webhook (payload version 4).
//   - uptime-kuma: the "Webhook" notification of Uptime Kuma (JSON body).
//   - gatus: Gatus "custom" alert provider. Gatus sends whatever body template
//     the operator configures, so LCM defines the expected shape:
//     {"endpoint":"[ENDPOINT_NAME]","group":"[ENDPOINT_GROUP]",
//     "url":"[ENDPOINT_URL]","description":"[ALERT_DESCRIPTION]",
//     "status":"[ALERT_TRIGGERED_OR_RESOLVED]"}; "resource" (or "service"),
//     "severity" and "message" are optional extra fields.
//   - generic: {"resource","alert","status":"firing|resolved","severity",
//     "labels","message"} as one object or an array of objects; "service"
//     is accepted in place of "resource".
//
// A payload without an explicit resource maps to one through the first
// label of Options.ResourceLabels that is present.
package alert

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/signal"
)

// Sources.
const (
	SourceAlertmanager = "alertmanager"
	SourceUptimeKuma   = "uptime-kuma"
	SourceGatus        = "gatus"
	SourceGeneric      = "generic"
)

// Alert states carried in Data[DataStatus].
const (
	StatusFiring   = "firing"
	StatusResolved = "resolved"
)

// Keys of signal.Signal.Data set by every parser.
const (
	DataAlert       = "alert"
	DataStatus      = "status"
	DataFingerprint = "fingerprint"
	DataMessage     = "message"
)

// DefaultResourceLabels are the labels that name the resource of an alert.
// The service labels stay accepted because existing alert rules use them.
var DefaultResourceLabels = []string{"lcm_resource", "resource", "lcm_service", "service"}

// Errors returned by Parse.
var (
	ErrUnknownSource  = errors.New("alert: unknown source")
	ErrInvalidPayload = errors.New("alert: invalid payload")
)

// Options tunes parsing.
type Options struct {
	// ResourceLabels lists the labels (or tag names) that carry the
	// resource name, in order of preference; nil selects
	// DefaultResourceLabels.
	ResourceLabels []string
}

func (o Options) resourceLabels() []string {
	if len(o.ResourceLabels) == 0 {
		return DefaultResourceLabels
	}
	return o.ResourceLabels
}

// resourceOf returns the first resource label present in labels.
func (o Options) resourceOf(labels map[string]string) string {
	for _, k := range o.resourceLabels() {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	return ""
}

// Sources returns the supported source names.
func Sources() []string {
	return []string{SourceAlertmanager, SourceGatus, SourceGeneric, SourceUptimeKuma}
}

// Parse converts the body received from source into alert signals. Signals
// have no ID or Time; the publisher fills them. A valid payload that carries
// no alert state (for example Uptime Kuma's test notification) yields no
// signals and no error.
func Parse(source string, body []byte, opts Options) ([]signal.Signal, error) {
	switch source {
	case SourceAlertmanager:
		return parseAlertmanager(body, opts)
	case SourceUptimeKuma:
		return parseUptimeKuma(body, opts)
	case SourceGatus:
		return parseGatus(body, opts)
	case SourceGeneric:
		return parseGeneric(body, opts)
	default:
		return nil, fmt.Errorf("%w %q (want one of %s)", ErrUnknownSource, source, strings.Join(Sources(), ", "))
	}
}

func invalid(source string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrInvalidPayload, source, err)
}

func invalidf(source, format string, args ...any) error {
	return invalid(source, fmt.Errorf(format, args...))
}

// Severity normalizes common severity words; unknown values yield def.
func normalizeSeverity(s, def string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "crit", "page", "error", "fatal", "high", "emergency", "alert":
		return signal.SeverityCritical
	case "warning", "warn", "medium", "major":
		return signal.SeverityWarning
	case "info", "informational", "low", "minor", "none", "notice":
		return signal.SeverityInfo
	default:
		return def
	}
}

// fingerprint derives a stable identity from parts when the source does not
// supply one.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// labelFingerprint hashes a label set independent of map order.
func labelFingerprint(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		parts = append(parts, k, labels[k])
	}
	return fingerprint(parts...)
}

// explicitResource returns the resource named by a payload field: resource
// is preferred, service is its older name.
func explicitResource(resource, service string) string {
	if r := strings.TrimSpace(resource); r != "" {
		return r
	}
	return strings.TrimSpace(service)
}

func build(source, resource, name, status, severity, message, fp string, labels map[string]string, extra map[string]any) signal.Signal {
	data := map[string]any{
		DataAlert:       name,
		DataStatus:      status,
		DataFingerprint: fp,
		DataMessage:     message,
	}
	for k, v := range extra {
		if v != "" && v != nil {
			data[k] = v
		}
	}
	return signal.Signal{
		Kind:     signal.KindAlert,
		Resource: resource,
		Source:   source,
		Severity: severity,
		Labels:   labels,
		Data:     data,
	}
}
