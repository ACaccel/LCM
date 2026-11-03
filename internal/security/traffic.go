package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/model"
)

// MaxTrafficRows keeps more than seven days of six minute-category windows,
// independently of incident/event retention.
const MaxTrafficRows = 100000

// Traffic is an immutable typed observation, with no alert/recovery semantics.
// Timings are milliseconds; byte counters come from sanitized Nginx logs.
type Traffic struct {
	MachineID        string    `json:"machineId"`
	Service          string    `json:"service,omitempty"`
	WindowStart      time.Time `json:"windowStart"`
	WindowEnd        time.Time `json:"windowEnd"`
	Category         string    `json:"category"`
	Requests         int64     `json:"requests"`
	Status429        int64     `json:"status429"`
	Status5xx        int64     `json:"status5xx"`
	RequestTimeSumMs float64   `json:"requestTimeSumMs"`
	RequestTimeMaxMs float64   `json:"requestTimeMaxMs"`
	ResponseBytes    int64     `json:"responseBytes"`
	RequestBytes     int64     `json:"requestBytes"`
	DryRunHits       int64     `json:"dryRunHits"`
}

// TrafficRecord is the stored observation with a stable pagination identity.
type TrafficRecord struct {
	ID int64 `json:"id"`
	Traffic
}

// TrafficReceipt confirms storage, including an identical collector retry.
type TrafficReceipt struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
}

func (m *Manager) validateTraffic(v Traffic) error {
	var issues model.Issues
	issue := func(p, msg string) { issues = append(issues, model.Issue{Path: p, Message: msg}) }
	if v.MachineID == "" || len(v.MachineID) > 100 {
		issue("machineId", "immutable Machine id is required")
	}
	if v.Service != "" && (len(v.Service) > 63 || !serviceName.MatchString(v.Service)) {
		issue("service", "invalid Service name")
	}
	switch v.Category {
	case "auth", "api", "upload", "socket", "static", "other":
	default:
		issue("category", "unsupported traffic category")
	}
	now := m.cfg.Clock.Now()
	if v.WindowStart.IsZero() || !v.WindowEnd.After(v.WindowStart) || v.WindowEnd.Sub(v.WindowStart) > time.Hour || v.WindowEnd.After(now.Add(5*time.Minute)) || v.WindowStart.Before(now.Add(-m.cfg.Retention)) {
		issue("windowStart", "window must be positive, at most one hour, and within retained history/future skew")
	}
	for name, n := range map[string]int64{"requests": v.Requests, "status429": v.Status429, "status5xx": v.Status5xx, "responseBytes": v.ResponseBytes, "requestBytes": v.RequestBytes, "dryRunHits": v.DryRunHits} {
		if n < 0 || n > 1e12 {
			issue(name, "counter must be between zero and 1000000000000")
		}
	}
	if v.Status429+v.Status5xx > v.Requests || v.DryRunHits > v.Requests {
		issue("requests", "subcounters cannot exceed requests")
	}
	for name, n := range map[string]float64{"requestTimeSumMs": v.RequestTimeSumMs, "requestTimeMaxMs": v.RequestTimeMaxMs} {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e15 {
			issue(name, "timing must be a finite non-negative millisecond value")
		}
	}
	if v.RequestTimeMaxMs > v.RequestTimeSumMs || (v.Requests == 0 && (v.RequestTimeSumMs != 0 || v.RequestTimeMaxMs != 0)) {
		issue("requestTimeMaxMs", "maximum cannot exceed the sum; empty windows have zero timings")
	}
	return issues.Err()
}

// IngestTraffic persists normal metrics without opening or recovering incidents.
func (m *Manager) IngestTraffic(ctx context.Context, v Traffic) (TrafficReceipt, error) {
	if !m.cfg.Enabled {
		return TrafficReceipt{}, ErrDisabled
	}
	v.WindowStart = v.WindowStart.UTC()
	v.WindowEnd = v.WindowEnd.UTC()
	if err := m.validateTraffic(v); err != nil {
		return TrafficReceipt{}, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return TrafficReceipt{}, err
	}
	digest := sha256.Sum256(body)
	identity, _ := json.Marshal([]any{v.MachineID, v.Service, v.Category, v.WindowStart, v.WindowEnd})
	key := sha256.Sum256(identity)
	keyText, digestText := hex.EncodeToString(key[:]), hex.EncodeToString(digest[:])
	receipt := TrafficReceipt{Accepted: true}
	err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT digest FROM security_traffic WHERE observation_key=?`, keyText).Scan(&existing)
		if err == nil {
			if existing != digestText {
				return ErrConflict
			}
			receipt.Duplicate = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE id=? AND phase='managed'`, v.MachineID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return model.Issues{{Path: "machineId", Message: "registered managed Machine id is required"}}
		}
		if v.Service != "" {
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE name=?`, v.Service).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return model.Issues{{Path: "service", Message: "Service does not exist"}}
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO security_traffic(observation_key,digest,machine_id,service,category,window_start,window_end,received_at,body) VALUES(?,?,?,?,?,?,?,?,?)`, keyText, digestText, v.MachineID, v.Service, v.Category, v.WindowStart.UnixMilli(), v.WindowEnd.UnixMilli(), m.cfg.Clock.Now().UnixMilli(), string(body))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM security_traffic WHERE id IN (SELECT id FROM security_traffic ORDER BY id DESC LIMIT -1 OFFSET ?)`, MaxTrafficRows)
		return err
	})
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
	}
	if auditErr := m.cfg.Audit.Record(ctx, "security.traffic", keyText, outcome, map[string]any{"machineId": v.MachineID, "category": v.Category, "duplicate": receipt.Duplicate}); auditErr != nil {
		return receipt, auditErr
	}
	return receipt, err
}

// TrafficHistory returns typed minute windows newest-first by receipt id.
func (m *Manager) TrafficHistory(ctx context.Context, before int64, limit int) ([]TrafficRecord, error) {
	if limit < 1 || limit > 501 {
		return nil, invalidConfig("traffic page size")
	}
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT id,body FROM security_traffic WHERE (?=0 OR id<?) ORDER BY id DESC LIMIT ?`, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TrafficRecord{}
	for rows.Next() {
		var record TrafficRecord
		var raw string
		if err = rows.Scan(&record.ID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &record.Traffic); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}
