package machine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// Workload is the registered impact inventory, not proof of shutdown readiness.
type Workload struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	State     string `json:"state"`
	Autostart string `json:"autostart"`
}

type MaintenancePreview struct {
	MachineID         string     `json:"machineId"`
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
	p := MaintenancePreview{MachineID: r.ID, Version: r.Version, PartialCoverage: true, ControllerOffline: true, ManualPowerOn: true, Workloads: []Workload{}}
	rows, err := q.QueryContext(ctx, `SELECT s.name,s.version,s.state FROM services s JOIN hosts h ON h.name=s.host WHERE h.kind='local' ORDER BY s.name`)
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
