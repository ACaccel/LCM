package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
)

// ErrPolicyNotFound means no version of the policy has ever been stored.
var ErrPolicyNotFound = errors.New("engine: policy not found")

// Default and maximum page sizes of ListRuns.
const (
	DefaultRunLimit = 50
	MaxRunLimit     = 500
)

// Valid reports whether s is a known run status.
func (s RunStatus) Valid() bool {
	switch s {
	case RunQueued, RunRunning, RunSuspended, RunSucceeded, RunFailed, RunCancelled, RunInterrupted, RunDropped:
		return true
	}
	return false
}

// RunQuery selects runs for ListRuns. Zero fields select everything.
type RunQuery struct {
	Policy string
	// Statuses, when not empty, keeps only runs in one of these statuses.
	Statuses []RunStatus
	// Limit is the page size; 0 selects DefaultRunLimit.
	Limit int
	// Before is the id of the last run of the previous page.
	Before string
}

// RunSummary is a run without its per-node trace.
type RunSummary struct {
	ID          string     `json:"id"`
	Policy      string     `json:"policy"`
	Version     int        `json:"version"`
	Status      RunStatus  `json:"status"`
	Mode        Mode       `json:"mode"`
	TriggerNode string     `json:"triggerNode"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// RunPage is one page of runs, newest first. Next is the Before value of
// the following page, empty on the last one.
type RunPage struct {
	Runs []RunSummary
	Next string
}

// ListRuns returns stored runs, newest first. Status is read from storage,
// which trails the in-memory state of an active run by at most one
// checkpoint commit.
func (e *Engine) ListRuns(ctx context.Context, q RunQuery) (RunPage, error) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultRunLimit
	case limit > MaxRunLimit:
		limit = MaxRunLimit
	}
	var (
		where []string
		args  []any
	)
	if q.Policy != "" {
		where = append(where, "policy = ?")
		args = append(args, q.Policy)
	}
	if len(q.Statuses) > 0 {
		for _, s := range q.Statuses {
			if !s.Valid() {
				return RunPage{}, fmt.Errorf("engine: unknown run status %q", s)
			}
			args = append(args, string(s))
		}
		where = append(where, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Statuses)), ",")+")")
	}
	if q.Before != "" {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	query := `SELECT ` + runColumns + ` FROM runs`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := e.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return RunPage{}, fmt.Errorf("engine: list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := RunPage{Runs: []RunSummary{}}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return RunPage{}, fmt.Errorf("engine: list runs: %w", err)
		}
		s := RunSummary{
			ID: r.id, Policy: r.policy, Version: r.version, Status: r.status, Mode: r.mode,
			TriggerNode: r.triggerNode, Error: r.err, CreatedAt: r.createdAt,
		}
		if !r.startedAt.IsZero() {
			t := r.startedAt
			s.StartedAt = &t
		}
		if !r.finishedAt.IsZero() {
			t := r.finishedAt
			s.FinishedAt = &t
		}
		page.Runs = append(page.Runs, s)
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, fmt.Errorf("engine: list runs: %w", err)
	}
	if len(page.Runs) > limit {
		page.Runs = page.Runs[:limit]
		page.Next = page.Runs[limit-1].ID
	}
	return page, nil
}

// PolicyInfo summarizes a policy across its stored versions.
type PolicyInfo struct {
	Name          string
	LatestVersion int
	// DeployedVersion is 0 when the policy is not deployed.
	DeployedVersion int
	Paused          bool
	// DeployedAt is zero when the policy is not deployed.
	DeployedAt time.Time
	// UpdatedAt is when the latest version was stored.
	UpdatedAt time.Time
}

// Policies lists every policy that has a stored version, deployed or not,
// ordered by name.
func (e *Engine) Policies(ctx context.Context) ([]PolicyInfo, error) {
	rows, err := e.db.Reader().QueryContext(ctx,
		`SELECT v.name, v.version, v.created_at, COALESCE(d.version, 0), COALESCE(d.paused, 0), d.deployed_at
		 FROM policy_versions v
		 LEFT JOIN policy_deployments d ON d.name = v.name
		 WHERE v.version = (SELECT MAX(version) FROM policy_versions WHERE name = v.name)
		 ORDER BY v.name`)
	if err != nil {
		return nil, fmt.Errorf("engine: list policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PolicyInfo
	for rows.Next() {
		var (
			p        PolicyInfo
			created  int64
			deployed *int64
		)
		if err := rows.Scan(&p.Name, &p.LatestVersion, &created, &p.DeployedVersion, &p.Paused, &deployed); err != nil {
			return nil, fmt.Errorf("engine: list policies: %w", err)
		}
		p.UpdatedAt = time.UnixMilli(created).UTC()
		if deployed != nil {
			p.DeployedAt = time.UnixMilli(*deployed).UTC()
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list policies: %w", err)
	}
	return out, nil
}

// PolicyVersionInfo describes one stored policy version.
type PolicyVersionInfo struct {
	Version   int
	CreatedAt time.Time
	CreatedBy auth.Actor
}

// PolicyVersion is a stored version with its document (canonical JSON) and
// the module documents it was compiled with, keyed by module name.
type PolicyVersion struct {
	PolicyVersionInfo
	Name     string
	Document []byte
	Modules  map[string][]byte
}

// PolicyVersions lists the stored versions of a policy, newest first.
func (e *Engine) PolicyVersions(ctx context.Context, name string) ([]PolicyVersionInfo, error) {
	rows, err := e.db.Reader().QueryContext(ctx,
		`SELECT version, created_at, created_by_kind, created_by_id, created_by_name
		 FROM policy_versions WHERE name = ? ORDER BY version DESC`, name)
	if err != nil {
		return nil, fmt.Errorf("engine: list policy versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PolicyVersionInfo
	for rows.Next() {
		var (
			v    PolicyVersionInfo
			ms   int64
			kind string
		)
		if err := rows.Scan(&v.Version, &ms, &kind, &v.CreatedBy.ID, &v.CreatedBy.Name); err != nil {
			return nil, fmt.Errorf("engine: list policy versions: %w", err)
		}
		v.CreatedAt, v.CreatedBy.Kind = time.UnixMilli(ms).UTC(), auth.Kind(kind)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("engine: list policy versions: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPolicyNotFound, name)
	}
	return out, nil
}

// PolicyVersion returns one stored version; version 0 selects the newest.
func (e *Engine) PolicyVersion(ctx context.Context, name string, version int) (PolicyVersion, error) {
	query := `SELECT version, document, modules, created_at, created_by_kind, created_by_id, created_by_name
		 FROM policy_versions WHERE name = ?`
	args := []any{name}
	if version > 0 {
		query += ` AND version = ?`
		args = append(args, version)
	} else {
		query += ` ORDER BY version DESC LIMIT 1`
	}
	var (
		v         = PolicyVersion{Name: name}
		doc, mods string
		ms        int64
		kind      string
	)
	err := e.db.Reader().QueryRowContext(ctx, query, args...).
		Scan(&v.Version, &doc, &mods, &ms, &kind, &v.CreatedBy.ID, &v.CreatedBy.Name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyVersion{}, fmt.Errorf("%w: %s version %d", ErrPolicyNotFound, name, version)
		}
		return PolicyVersion{}, fmt.Errorf("engine: get policy version: %w", err)
	}
	row, err := decodeVersion(name, v.Version, doc, mods)
	if err != nil {
		return PolicyVersion{}, err
	}
	v.CreatedAt, v.CreatedBy.Kind = time.UnixMilli(ms).UTC(), auth.Kind(kind)
	v.Document, v.Modules = row.document, row.modules
	return v, nil
}
