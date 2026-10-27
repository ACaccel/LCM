package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

// Stored node statuses (run_nodes.status).
const (
	rowRunning   = "running"
	rowRetrying  = "retrying"
	rowSuspended = "suspended"
	rowResumed   = "resumed"
	rowCompleted = "completed"
	rowFailed    = "failed"
)

func msOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func timeOf(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

func bytesOrNil(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// nodeRec upserts a run_nodes row.
type nodeRec struct {
	runID, nodeID string
	status        string
	port          string
	props         []byte
	output        []byte
	attempts      int
	err           string
	startedAt     time.Time
	finishedAt    time.Time
	suspendKey    string
	resumeAt      time.Time
	resumePayload []byte
}

func (r *nodeRec) exec(ctx context.Context, st *stmts) error {
	err := st.exec(ctx, `INSERT INTO run_nodes
		(run_id, node_id, status, port, props, output, attempts, error, started_at, finished_at, suspend_key, resume_at, resume_payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, node_id) DO UPDATE SET
		status = excluded.status, port = excluded.port, props = excluded.props, output = excluded.output,
		attempts = excluded.attempts, error = excluded.error, started_at = excluded.started_at,
		finished_at = excluded.finished_at, suspend_key = excluded.suspend_key, resume_at = excluded.resume_at,
		resume_payload = excluded.resume_payload`,
		r.runID, r.nodeID, r.status, r.port, bytesOrNil(r.props), bytesOrNil(r.output), r.attempts, r.err,
		msOrNil(r.startedAt), msOrNil(r.finishedAt), r.suspendKey, msOrNil(r.resumeAt), bytesOrNil(r.resumePayload))
	if err != nil {
		return fmt.Errorf("checkpoint node %s/%s: %w", r.runID, r.nodeID, err)
	}
	return nil
}

// runRec updates a run's status. A zero startedAt keeps the stored value.
type runRec struct {
	id         string
	status     RunStatus
	err        string
	startedAt  time.Time
	finishedAt time.Time
}

func (r *runRec) exec(ctx context.Context, st *stmts) error {
	err := st.exec(ctx, `UPDATE runs SET status = ?, error = ?,
		started_at = COALESCE(?, started_at), finished_at = ? WHERE id = ?`,
		string(r.status), r.err, msOrNil(r.startedAt), msOrNil(r.finishedAt), r.id)
	if err != nil {
		return fmt.Errorf("checkpoint run %s: %w", r.id, err)
	}
	return nil
}

type runRow struct {
	id, policy  string
	version     int
	status      RunStatus
	triggerNode string
	event       []byte
	mode        Mode
	createdAt   time.Time
	startedAt   time.Time
	finishedAt  time.Time
	err         string
}

func insertRun(ctx context.Context, db *store.DB, r runRow) error {
	err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO runs
			(id, policy, version, status, trigger_node, event, mode, created_at, started_at, finished_at, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.policy, r.version, string(r.status), r.triggerNode, string(r.event), string(r.mode),
			r.createdAt.UnixMilli(), msOrNil(r.startedAt), msOrNil(r.finishedAt), r.err)
		return err
	})
	if err != nil {
		return fmt.Errorf("engine: insert run: %w", err)
	}
	return nil
}

const runColumns = `id, policy, version, status, trigger_node, event, mode, created_at, started_at, finished_at, error`

func scanRun(sc interface{ Scan(...any) error }) (runRow, error) {
	var (
		r                   runRow
		status, mode, event string
		created             int64
		started, finished   sql.NullInt64
	)
	if err := sc.Scan(&r.id, &r.policy, &r.version, &status, &r.triggerNode, &event, &mode,
		&created, &started, &finished, &r.err); err != nil {
		return runRow{}, err
	}
	r.status, r.mode, r.event = RunStatus(status), Mode(mode), []byte(event)
	r.createdAt = time.UnixMilli(created).UTC()
	r.startedAt, r.finishedAt = timeOf(started), timeOf(finished)
	return r, nil
}

func loadRun(ctx context.Context, db *store.DB, id string) (runRow, error) {
	r, err := scanRun(db.Reader().QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return runRow{}, ErrRunNotFound
	}
	if err != nil {
		return runRow{}, fmt.Errorf("engine: load run: %w", err)
	}
	return r, nil
}

func loadActiveRuns(ctx context.Context, db *store.DB) ([]runRow, error) {
	rows, err := db.Reader().QueryContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE status IN ('queued', 'running', 'suspended') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("engine: load active runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []runRow
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("engine: load active runs: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: load active runs: %w", err)
	}
	return out, nil
}

type nodeRow struct {
	status        string
	port          string
	props         []byte
	output        []byte
	attempts      int
	err           string
	startedAt     time.Time
	finishedAt    time.Time
	suspendKey    string
	resumeAt      time.Time
	resumePayload []byte
}

func loadNodeRows(ctx context.Context, db *store.DB, runID string) (map[string]nodeRow, error) {
	rows, err := db.Reader().QueryContext(ctx, `SELECT node_id, status, port, props, output, attempts, error,
		started_at, finished_at, suspend_key, resume_at, resume_payload FROM run_nodes WHERE run_id = ?`, runID)
	if err != nil {
		return nil, fmt.Errorf("engine: load run nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]nodeRow{}
	for rows.Next() {
		var (
			id                       string
			n                        nodeRow
			props, output, payload   sql.NullString
			started, finished, resum sql.NullInt64
		)
		if err := rows.Scan(&id, &n.status, &n.port, &props, &output, &n.attempts, &n.err,
			&started, &finished, &n.suspendKey, &resum, &payload); err != nil {
			return nil, fmt.Errorf("engine: load run nodes: %w", err)
		}
		if props.Valid {
			n.props = []byte(props.String)
		}
		if output.Valid {
			n.output = []byte(output.String)
		}
		if payload.Valid {
			n.resumePayload = []byte(payload.String)
		}
		n.startedAt, n.finishedAt, n.resumeAt = timeOf(started), timeOf(finished), timeOf(resum)
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: load run nodes: %w", err)
	}
	return out, nil
}

// versionRow is a stored policy version.
type versionRow struct {
	name     string
	version  int
	document []byte
	modules  map[string][]byte
}

func loadVersion(ctx context.Context, db *store.DB, name string, version int) (versionRow, error) {
	var doc, mods string
	err := db.Reader().QueryRowContext(ctx,
		`SELECT document, modules FROM policy_versions WHERE name = ? AND version = ?`, name, version).Scan(&doc, &mods)
	if errors.Is(err, sql.ErrNoRows) {
		return versionRow{}, fmt.Errorf("engine: policy %s version %d not found", name, version)
	}
	if err != nil {
		return versionRow{}, fmt.Errorf("engine: load policy version: %w", err)
	}
	return decodeVersion(name, version, doc, mods)
}

func decodeVersion(name string, version int, doc, mods string) (versionRow, error) {
	v := versionRow{name: name, version: version, document: []byte(doc)}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(mods), &raw); err != nil {
		return versionRow{}, fmt.Errorf("engine: decode modules of %s v%d: %w", name, version, err)
	}
	v.modules = make(map[string][]byte, len(raw))
	for k, m := range raw {
		v.modules[k] = m
	}
	return v, nil
}

type deploymentRow struct {
	versionRow
	paused bool
}

func loadDeployments(ctx context.Context, db *store.DB) ([]deploymentRow, error) {
	rows, err := db.Reader().QueryContext(ctx, `SELECT d.name, d.version, d.paused, v.document, v.modules
		FROM policy_deployments d JOIN policy_versions v ON v.name = d.name AND v.version = d.version
		ORDER BY d.name`)
	if err != nil {
		return nil, fmt.Errorf("engine: load deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []deploymentRow
	for rows.Next() {
		var (
			name, doc, mods string
			version         int
			paused          bool
		)
		if err := rows.Scan(&name, &version, &paused, &doc, &mods); err != nil {
			return nil, fmt.Errorf("engine: load deployments: %w", err)
		}
		v, err := decodeVersion(name, version, doc, mods)
		if err != nil {
			return nil, err
		}
		out = append(out, deploymentRow{versionRow: v, paused: paused})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: load deployments: %w", err)
	}
	return out, nil
}

// storeVersion stores plan as a new version unless it equals the latest
// version, and marks it deployed. It returns the deployed version, whether
// it already existed, and the paused flag.
func storeVersion(ctx context.Context, db *store.DB, plan *Plan, actor auth.Actor, now time.Time) (version int, unchanged, paused bool, err error) {
	mods, err := json.Marshal(rawModules(plan.modules))
	if err != nil {
		return 0, false, false, fmt.Errorf("engine: encode modules: %w", err)
	}
	err = db.Write(ctx, func(tx *sql.Tx) error {
		var latest sql.NullInt64
		var doc, latestMods sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT version, document, modules FROM policy_versions
			WHERE name = ? ORDER BY version DESC LIMIT 1`, plan.name).Scan(&latest, &doc, &latestMods)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if latest.Valid && doc.String == string(plan.document) && latestMods.String == string(mods) {
			version, unchanged = int(latest.Int64), true
		} else {
			version = int(latest.Int64) + 1
			if _, err := tx.ExecContext(ctx, `INSERT INTO policy_versions
				(name, version, document, modules, created_at, created_by_kind, created_by_id, created_by_name)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				plan.name, version, string(plan.document), string(mods), now.UnixMilli(),
				string(actor.Kind), actor.ID, actor.Name); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO policy_deployments (name, version, paused, deployed_at)
			VALUES (?, ?, 0, ?) ON CONFLICT (name) DO UPDATE SET version = excluded.version, deployed_at = excluded.deployed_at`,
			plan.name, version, now.UnixMilli()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT paused FROM policy_deployments WHERE name = ?`, plan.name).Scan(&paused)
	})
	if err != nil {
		return 0, false, false, fmt.Errorf("engine: store policy version: %w", err)
	}
	return version, unchanged, paused, nil
}

// rawModules lets json.Marshal embed stored module documents verbatim;
// json.Marshal sorts map keys, so equal module sets encode identically.
func rawModules(m map[string][]byte) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func decodeStoredPolicy(v versionRow) (*model.Policy, StaticModules, error) {
	p, err := model.DecodePolicy(v.document)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: decode stored policy %s v%d: %w", v.name, v.version, err)
	}
	mods := StaticModules{}
	for name, b := range v.modules {
		m, err := model.DecodeModule(b)
		if err != nil {
			return nil, nil, fmt.Errorf("engine: decode stored module %s of %s v%d: %w", name, v.name, v.version, err)
		}
		mods[name] = m
	}
	return p, mods, nil
}
