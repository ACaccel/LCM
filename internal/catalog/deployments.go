package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
)

// Deployment outcomes.
const (
	OutcomeRunning     = "running"
	OutcomeSucceeded   = "succeeded"
	OutcomeFailed      = "failed"
	OutcomeInterrupted = "interrupted"
)

// Caps on stored text; deployment logs keep their tail, where failures show.
const (
	maxDeploymentLog   = 16 << 10
	maxDeploymentError = 2 << 10
)

// Deployment is one deploy or rollback of a service.
type Deployment struct {
	ID      int64
	Service string
	// Action is "deploy" or "rollback".
	Action string
	// Version is the commit sha that was deployed.
	Version string
	// PreviousVersion is the version it replaced, when known.
	PreviousVersion string
	StartedAt       time.Time
	FinishedAt      time.Time
	Outcome         string
	Actor           auth.Actor
	RunID           string
	Error           string
	LogExcerpt      string
}

// BeginDeployment records a deployment as running and returns its id.
func (c *Catalog) BeginDeployment(ctx context.Context, d Deployment) (int64, error) {
	if d.StartedAt.IsZero() {
		d.StartedAt = c.now()
	}
	var id int64
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (service, action, version, previous_version, started_at, outcome, actor_kind, actor_id, actor_name, run_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Service, d.Action, d.Version, d.PreviousVersion, d.StartedAt.UnixMilli(), OutcomeRunning,
			string(d.Actor.Kind), d.Actor.ID, d.Actor.Name, d.RunID)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("catalog: begin deployment: %w", err)
	}
	return id, nil
}

// SetDeploymentVersions records the resolved commit once it is known.
func (c *Catalog) SetDeploymentVersions(ctx context.Context, id int64, version, previous string) error {
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET version = ?, previous_version = ? WHERE id = ?`, version, previous, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog: update deployment: %w", err)
	}
	return nil
}

// FinishDeployment closes a deployment with its outcome. Only running
// deployments can be finished.
func (c *Catalog) FinishDeployment(ctx context.Context, id int64, outcome, errMsg, log string) error {
	switch outcome {
	case OutcomeSucceeded, OutcomeFailed, OutcomeInterrupted:
	default:
		return fmt.Errorf("catalog: invalid deployment outcome %q", outcome)
	}
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET outcome = ?, finished_at = ?, error = ?, log = ? WHERE id = ? AND outcome = ?`,
			outcome, c.now().UnixMilli(), headCap(errMsg, maxDeploymentError), tailCap(log, maxDeploymentLog), id, OutcomeRunning)
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog: finish deployment: %w", err)
	}
	return nil
}

// Deployments lists a service's deployments newest first. before, when
// positive, returns only ids below it.
func (c *Catalog) Deployments(ctx context.Context, service string, limit int, before int64) ([]Deployment, error) {
	return c.deployments(ctx, `WHERE service = ?`+beforeClause(before), limit, append([]any{service}, beforeArgs(before)...)...)
}

// RunningDeployments lists deployments that have no outcome yet, across all
// services. After a restart these are the interrupted ones.
func (c *Catalog) RunningDeployments(ctx context.Context) ([]Deployment, error) {
	return c.deployments(ctx, `WHERE outcome = 'running'`, 1000)
}

func beforeClause(before int64) string {
	if before > 0 {
		return " AND id < ?"
	}
	return ""
}

func beforeArgs(before int64) []any {
	if before > 0 {
		return []any{before}
	}
	return nil
}

func (c *Catalog) deployments(ctx context.Context, where string, limit int, args ...any) ([]Deployment, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := c.db.Reader().QueryContext(ctx,
		`SELECT id, service, action, version, previous_version, started_at, finished_at, outcome,
		        actor_kind, actor_id, actor_name, run_id, error, log
		 FROM deployments `+where+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("catalog: query deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Deployment
	for rows.Next() {
		var (
			d        Deployment
			started  int64
			finished sql.NullInt64
			kind     string
		)
		if err := rows.Scan(&d.ID, &d.Service, &d.Action, &d.Version, &d.PreviousVersion, &started, &finished, &d.Outcome,
			&kind, &d.Actor.ID, &d.Actor.Name, &d.RunID, &d.Error, &d.LogExcerpt); err != nil {
			return nil, fmt.Errorf("catalog: scan deployment: %w", err)
		}
		d.StartedAt = time.UnixMilli(started).UTC()
		if finished.Valid {
			d.FinishedAt = time.UnixMilli(finished.Int64).UTC()
		}
		d.Actor.Kind = auth.Kind(kind)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: query deployments: %w", err)
	}
	return out, nil
}

// rollbackTarget picks the version a rollback should redeploy from a
// service's deployments, newest first.
//
// When the latest deployment failed, the service should return to the last
// version that worked. When it succeeded, the target is the most recent
// successful deployment of a different version. Interrupted and running
// deployments are ignored: their outcome is unknown.
func rollbackTarget(deps []Deployment) (version string, ok bool) {
	var latest *Deployment
	for i := range deps {
		if deps[i].Outcome == OutcomeSucceeded || deps[i].Outcome == OutcomeFailed {
			latest = &deps[i]
			break
		}
	}
	if latest == nil {
		return "", false
	}
	for _, d := range deps {
		if d.Outcome != OutcomeSucceeded || d.Version == "" {
			continue
		}
		if latest.Outcome == OutcomeFailed || d.Version != latest.Version {
			return d.Version, true
		}
	}
	return "", false
}

// lastSuccessful returns the version of the newest successful deployment.
func lastSuccessful(deps []Deployment) string {
	for _, d := range deps {
		if d.Outcome == OutcomeSucceeded && d.Version != "" {
			return d.Version
		}
	}
	return ""
}

// completion records.

// CompletedAction returns the stored result of the last completed action
// when its idempotency key equals key.
func (c *Catalog) CompletedAction(ctx context.Context, service, action, key string) ([]byte, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	var stored, result string
	err := c.db.Reader().QueryRowContext(ctx,
		`SELECT idem_key, result FROM action_completions WHERE service = ? AND action = ?`, service, action).Scan(&stored, &result)
	switch {
	case err == sql.ErrNoRows:
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("catalog: read completion: %w", err)
	case stored != key:
		return nil, false, nil
	}
	return []byte(result), true, nil
}

// RecordCompletion remembers that action finished under key, replacing the
// previous key of the same service and action.
func (c *Catalog) RecordCompletion(ctx context.Context, service, action, key string, result []byte) error {
	if key == "" {
		return nil
	}
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO action_completions (service, action, idem_key, completed_at, result) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (service, action) DO UPDATE SET idem_key = excluded.idem_key,
			   completed_at = excluded.completed_at, result = excluded.result`,
			service, action, key, c.now().UnixMilli(), string(result))
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog: record completion: %w", err)
	}
	return nil
}

// tailCap keeps the last n bytes of s, cutting at a line start when it can.
func tailCap(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "... " + strings.ToValidUTF8(s, "")
}

// headCap keeps the first n bytes of s.
func headCap(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + " ..."
}
