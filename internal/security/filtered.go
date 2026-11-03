package security

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/ACaccel/lcm/internal/model"
)

// IncidentFilter selects immutable detector state, independently of operator handling.
// Observation timestamps use the half-open interval [From, To).
type IncidentFilter struct {
	MachineID, Service, Source, Severity, State string
	From, To                                    time.Time
}

// TrafficFilter selects only complete observations contained in [From, To].
// Empty Service includes all services; __unassigned__ selects the empty service.
type TrafficFilter struct {
	MachineID, Service, Category string
	From, To                     time.Time
}

func filterIssue(path, message string) error {
	return model.Issues{{Path: path, Message: message}}
}

func validateQueryScope(machineID, service string, from, to time.Time) error {
	if len(machineID) > 100 || strings.TrimSpace(machineID) != machineID || strings.ContainsFunc(machineID, unicode.IsControl) {
		return filterIssue("machineId", "invalid immutable Machine id")
	}
	if service != "" && service != "__unassigned__" && (len(service) > 63 || !serviceName.MatchString(service)) {
		return filterIssue("service", "invalid Service name")
	}
	for _, field := range []struct {
		name  string
		value time.Time
	}{{"from", from}, {"to", to}} {
		if !field.value.IsZero() && (field.value.Year() < 1970 || field.value.Year() > 9999) {
			return filterIssue(field.name, "timestamp must be between 1970 and 9999")
		}
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return filterIssue("to", "must be after from")
	}
	return nil
}

func validateQueryPage(before int64, limit int) error {
	if before < 0 {
		return filterIssue("cursor", "cursor must be non-negative")
	}
	if limit < 1 || limit > 501 {
		return filterIssue("limit", "page size must be between 1 and 501")
	}
	return nil
}

func (f IncidentFilter) validate() error {
	if err := validateQueryScope(f.MachineID, f.Service, f.From, f.To); err != nil {
		return err
	}
	if f.Source != "" && f.Source != "machine" && f.Source != "service" && f.Source != "nginx" && f.Source != "auditd" && f.Source != "aide" && f.Source != "collector" {
		return filterIssue("source", "unsupported security source")
	}
	if f.Severity != "" && f.Severity != "info" && f.Severity != "warning" && f.Severity != "critical" {
		return filterIssue("severity", "unsupported security severity")
	}
	if f.State != "" && f.State != "firing" && f.State != "resolved" && f.State != "retired" {
		return filterIssue("state", "unsupported incident state")
	}
	return nil
}

func (f TrafficFilter) validate() error {
	if err := validateQueryScope(f.MachineID, f.Service, f.From, f.To); err != nil {
		return err
	}
	if f.Category != "" && f.Category != "auth" && f.Category != "api" && f.Category != "upload" && f.Category != "socket" && f.Category != "static" && f.Category != "other" {
		return filterIssue("category", "unsupported traffic category")
	}
	return nil
}

func appendScopeFilter(where []string, args []any, machineID, service string) ([]string, []any) {
	if machineID != "" {
		where, args = append(where, "machine_id=?"), append(args, machineID)
	}
	if service != "" {
		if service == "__unassigned__" {
			service = ""
		}
		where, args = append(where, "service=?"), append(args, service)
	}
	return where, args
}

// ListFiltered returns stable, newest-first incident pages. Date filtering uses
// the latest detector observation, not receipt time or an operator's edit time.
func (m *Manager) ListFiltered(ctx context.Context, f IncidentFilter, before int64, limit int) ([]Incident, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if err := validateQueryPage(before, limit); err != nil {
		return nil, err
	}
	where, args := appendScopeFilter([]string{"(?=0 OR seq<?)"}, []any{before, before}, f.MachineID, f.Service)
	for _, field := range []struct{ column, value string }{{"source", f.Source}, {"severity", f.Severity}, {"state", f.State}} {
		if field.value != "" {
			where, args = append(where, field.column+"=?"), append(args, field.value)
		}
	}
	if !f.From.IsZero() {
		where, args = append(where, "observed_at>=?"), append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		where, args = append(where, "observed_at<?"), append(args, f.To.UnixMilli())
	}
	args = append(args, limit)
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT `+incidentColumns+` FROM security_incidents WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []Incident{}
	for rows.Next() {
		item, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// IncidentByID preserves sql.ErrNoRows so the HTTP layer can return not found.
func (m *Manager) IncidentByID(ctx context.Context, id string) (Incident, error) {
	if id == "" || len(id) > 100 || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
		return Incident{}, filterIssue("id", "invalid incident id")
	}
	return scanIncident(m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM security_incidents WHERE id=?`, id))
}

func trafficFilterSQL(f TrafficFilter) ([]string, []any) {
	where, args := appendScopeFilter([]string{"1=1"}, nil, f.MachineID, f.Service)
	if f.Category != "" {
		where, args = append(where, "category=?"), append(args, f.Category)
	}
	if !f.From.IsZero() {
		where, args = append(where, "window_start>=?"), append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		where, args = append(where, "window_end<=?"), append(args, f.To.UnixMilli())
	}
	return where, args
}

// TrafficFiltered returns complete immutable windows newest-first by receipt id.
func (m *Manager) TrafficFiltered(ctx context.Context, f TrafficFilter, before int64, limit int) ([]TrafficRecord, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if err := validateQueryPage(before, limit); err != nil {
		return nil, err
	}
	where, args := trafficFilterSQL(f)
	where, args = append(where, "(?=0 OR id<?)"), append(args, before, before, limit)
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT id,body FROM security_traffic WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []TrafficRecord{}
	for rows.Next() {
		var item TrafficRecord
		var body string
		if err := rows.Scan(&item.ID, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &item.Traffic); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
