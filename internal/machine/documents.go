package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

func (m *Manager) Put(ctx context.Context, data []byte, expected int) (PutResult, error) {
	return m.put(ctx, data, expected, 0)
}

func (m *Manager) put(ctx context.Context, data []byte, expected, restored int) (PutResult, error) {
	a, err := actor(ctx)
	if err != nil {
		return PutResult{}, err
	}
	doc, err := model.DecodeMachine(data)
	if err != nil {
		return PutResult{}, err
	}
	if expected < 0 {
		return PutResult{}, precondition("expected version must not be negative")
	}
	ctx, release, err := m.gate.Acquire(ctx)
	if err != nil {
		return PutResult{}, err
	}
	defer release()
	canon, err := model.Encode(doc)
	if err != nil {
		return PutResult{}, err
	}
	now := m.clk.Now().UnixMilli()
	var result PutResult
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		var id, old string
		var version int
		err := tx.QueryRowContext(ctx, `SELECT m.id,m.version,v.document FROM machines m JOIN machine_versions v ON v.machine_id=m.id AND v.version=m.version WHERE m.phase!='unmanaged' AND m.name=?`, doc.Metadata.Name).Scan(&id, &version, &old)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if expected == 0 && found {
			return ErrExists
		}
		if expected > 0 && !found {
			return ErrNotFound
		}
		if found && expected != version {
			return ErrConflict
		}
		if found && old == string(canon) {
			result = PutResult{Version: version}
			return nil
		}
		if found {
			previous, err := model.DecodeMachine([]byte(old))
			if err != nil {
				return err
			}
			oldSelection, _ := encode(struct {
				Units   []string
				Journal bool
			}{previous.Spec.Monitoring.Units, previous.Spec.Monitoring.Journal})
			newSelection, _ := encode(struct {
				Units   []string
				Journal bool
			}{doc.Spec.Monitoring.Units, doc.Spec.Monitoring.Journal})
			if oldSelection != newSelection {
				if _, err := tx.ExecContext(ctx, `DELETE FROM machine_cursors WHERE machine_id=?`, id); err != nil {
					return err
				}
			}
		}
		if !found {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE phase!='unmanaged'`).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return ErrExists
			}
			id = newID()
			if _, err = tx.ExecContext(ctx, `INSERT INTO machines(id,name,version,created_at,updated_at) VALUES(?,?,?,?,?)`, id, doc.Metadata.Name, 1, now, now); err != nil {
				return err
			}
		}
		version++
		aJSON, err := encode(actorInfo(a))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO machine_versions(machine_id,version,document,restored_from,created_at,actor) VALUES(?,?,?,?,?,?)`, id, version, string(canon), restored, now, aJSON); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE machines SET version=?,updated_at=? WHERE id=?`, version, now, id); err != nil {
			return err
		}
		result = PutResult{Version: version, Created: !found, Changed: true}
		return nil
	})
	if err == nil && result.Changed {
		err = m.record(ctx, "save", doc.Metadata.Name, map[string]any{"version": result.Version, "restoredFrom": restored})
	}
	return result, err
}

func (m *Manager) List(ctx context.Context) ([]Record, error) { return m.query(ctx, false, "") }

// Archives exposes tombstones without mixing same-name incarnations.
func (m *Manager) Archives(ctx context.Context) ([]Record, error) {
	return m.query(ctx, true, " AND m.phase='unmanaged'")
}
func (m *Manager) Get(ctx context.Context, name string) (Record, error) {
	where, args := " AND m.name=?", []any{name}
	byID := false
	if id, ok := strings.CutPrefix(name, "@"); ok {
		where = " AND m.id=?"
		args = []any{id}
		byID = true
	}
	recs, err := m.query(ctx, byID, where, args...)
	if err != nil {
		return Record{}, err
	}
	if len(recs) == 0 {
		return Record{}, ErrNotFound
	}
	return recs[0], nil
}

func (m *Manager) query(ctx context.Context, includeArchive bool, where string, args ...any) ([]Record, error) {
	active := "m.phase!='unmanaged'"
	if includeArchive {
		active = "1=1"
	}
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT m.id,m.name,m.host,m.version,m.phase,m.created_at,m.updated_at,v.document,m.status FROM machines m JOIN machine_versions v ON v.machine_id=m.id AND v.version=m.version WHERE `+active+where+` ORDER BY m.name`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Record{}
	for rows.Next() {
		var r Record
		var created, updated int64
		var doc, status string
		if err = rows.Scan(&r.ID, &r.Name, &r.Host, &r.Version, &r.Phase, &created, &updated, &doc, &status); err != nil {
			return nil, err
		}
		if r.Document, err = model.DecodeMachine([]byte(doc)); err != nil {
			return nil, err
		}
		var persisted storedStatus
		if err = json.Unmarshal([]byte(status), &persisted); err != nil {
			return nil, err
		}
		r.Status = persisted.Status
		r.CreatedAt = time.UnixMilli(created).UTC()
		r.UpdatedAt = time.UnixMilli(updated).UTC()
		m.freshen(&r)
		if persisted.DocumentVersion != r.Version {
			r.Status.Fresh = false
			r.Status.Health = "unknown"
			r.Status.Reasons = []string{"configuration has not been observed"}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Manager) freshen(r *Record) {
	r.Status.Fresh = r.Status.Snapshot != nil && m.clk.Now().Sub(r.Status.Snapshot.ObservedAt) <= r.Document.Spec.Monitoring.StaleAfter.D() && !r.Status.Snapshot.ObservedAt.After(m.clk.Now())
	if r.Phase != "managed" {
		r.Status.Fresh = false
	}
	if !r.Status.Fresh {
		r.Status.Health = "unknown"
		r.Status.Reasons = []string{"observations missing or stale"}
	}
	if r.Status.Identity == "" {
		r.Status.Identity = "verification-required"
	}
	if r.Status.Journal.State == "" {
		r.Status.Journal = Coverage{State: "unknown", Reason: "not observed"}
	}
	if r.Status.Reasons == nil {
		r.Status.Reasons = []string{}
	}
}

func (m *Manager) Versions(ctx context.Context, name string) ([]Version, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT version,document,restored_from,created_at,actor FROM machine_versions WHERE machine_id=? ORDER BY version DESC`, r.ID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Version{}
	for rows.Next() {
		var v Version
		var doc, a string
		var at int64
		if err = rows.Scan(&v.Version, &doc, &v.RestoredFrom, &at, &a); err != nil {
			return nil, err
		}
		v.CreatedAt = time.UnixMilli(at).UTC()
		if v.Document, err = model.DecodeMachine([]byte(doc)); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(a), &v.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *Manager) Restore(ctx context.Context, name string, version, expected int) (PutResult, error) {
	if r, err := m.Get(ctx, name); err != nil {
		return PutResult{}, err
	} else if r.Phase == "unmanaged" {
		return PutResult{}, precondition("archived registrations are read only")
	}
	versions, err := m.Versions(ctx, name)
	if err != nil {
		return PutResult{}, err
	}
	for _, v := range versions {
		if v.Version == version {
			b, err := model.Encode(v.Document)
			if err != nil {
				return PutResult{}, err
			}
			return m.put(ctx, b, expected, version)
		}
	}
	return PutResult{}, ErrNotFound
}

// SetPhase retires or removes the active registration without changing OS power.
func (m *Manager) SetPhase(ctx context.Context, name, phase string, expected int) error {
	var machineID string
	if _, err := actor(ctx); err != nil {
		return err
	}
	if phase != "managed" && phase != "retired" && phase != "unmanaged" {
		return precondition("invalid management phase")
	}
	ctx, release, err := m.gate.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		var id string
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT id,version FROM machines WHERE name=? AND phase!='unmanaged'`, name).Scan(&id, &version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if version != expected {
			return ErrConflict
		}
		machineID = id
		var unresolved int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machine_operations WHERE machine_id=? AND unresolved=1`, id).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved > 0 {
			return precondition("unresolved operation")
		}
		if phase == "unmanaged" {
			var owned int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind='Machine' AND name=?`, name).Scan(&owned); err != nil {
				return err
			}
			if owned > 0 {
				return precondition("release Git ownership before unmanage")
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE machines SET phase=?,status='{}',updated_at=? WHERE id=?`, phase, m.clk.Now().UnixMilli(), id)
		return err
	})
	if err != nil {
		return err
	}
	return m.record(ctx, phase, name, map[string]any{"machineId": machineID})
}
