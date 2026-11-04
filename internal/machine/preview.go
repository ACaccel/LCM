package machine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// Workload is the registered impact inventory, not proof of shutdown
// readiness. Every managed service runs on the local machine.
type Workload struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	State     string `json:"state"`
	Autostart string `json:"autostart"`
}

type MaintenancePreview struct {
	ResourceID        string     `json:"resourceId"`
	Version           int        `json:"version"`
	Hash              string     `json:"hash"`
	PartialCoverage   bool       `json:"partialCoverage"`
	ControllerOffline bool       `json:"controllerOffline"`
	ManualPowerOn     bool       `json:"manualPowerOn"`
	Workloads         []Workload `json:"workloads"`
}

type previewReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func preview(ctx context.Context, q previewReader, r Record) (MaintenancePreview, error) {
	p := MaintenancePreview{ResourceID: r.ID, Version: r.Version, PartialCoverage: true, ControllerOffline: true, ManualPowerOn: true, Workloads: []Workload{}}
	rows, err := q.QueryContext(ctx, `SELECT r.name,r.version,s.state FROM resources r JOIN service_states s ON s.resource_id=r.id WHERE r.type='service' AND r.phase='managed' ORDER BY r.name`)
	if err != nil {
		return p, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		w := Workload{Autostart: "unknown; verify the supervisor configuration"}
		if err := rows.Scan(&w.Name, &w.Version, &w.State); err != nil {
			return p, err
		}
		p.Workloads = append(p.Workloads, w)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	h := sha256.Sum256(b)
	p.Hash = hex.EncodeToString(h[:])
	return p, nil
}

func (m *Manager) Preview(ctx context.Context, name string) (MaintenancePreview, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return MaintenancePreview{}, err
	}
	return preview(ctx, m.db.Reader(), r)
}
