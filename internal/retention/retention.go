// Package retention bounds the stored history that no single component can
// prune on its own: document versions (Resource, Policy, Module), whose
// references span the engine, config sync and the resource registry;
// finished operations (resource operations, security host operations,
// notification test episodes); and failed-login audit entries, the only
// audit entries that are not kept forever.
//
// Every rule is plain SQL that deletes in bounded batches, each batch in
// its own write transaction, so the single writer connection is never held
// for long. Anything still in use or unresolved is never deleted.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
)

// batch bounds the rows one statement deletes.
const batch = 1000

// Config configures a Pruner.
type Config struct {
	DB *store.DB
	// Clock defaults to the real clock.
	Clock clock.Clock
	// VersionKeep is how many of the newest versions of each document are
	// kept. Versions in use are kept beyond it.
	VersionKeep int
	// OperationAge is how long finished operations are kept after their
	// last update.
	OperationAge time.Duration
	// FailedLoginKeep is how many of the newest failed-login audit entries
	// are kept.
	FailedLoginKeep int
}

// Pruner runs the retention rules of this package.
type Pruner struct {
	cfg Config
}

// New validates cfg and returns a Pruner.
func New(cfg Config) (*Pruner, error) {
	if cfg.DB == nil {
		return nil, errors.New("retention: DB is required")
	}
	if cfg.VersionKeep < 1 || cfg.FailedLoginKeep < 1 || cfg.OperationAge <= 0 {
		return nil, errors.New("retention: keep counts must be at least 1 and the operation age positive")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	return &Pruner{cfg: cfg}, nil
}

// The version rules keep, per document, the newest VersionKeep versions
// (version numbers only grow, so every number above the newest minus
// VersionKeep is stored) and every older version that is still referenced.
var versionRules = []rule{
	{"resource versions", `DELETE FROM resource_versions WHERE (resource_id, version) IN (
		SELECT v.resource_id, v.version FROM resource_versions v JOIN resources r ON r.id = v.resource_id
		WHERE v.version <= (SELECT MAX(x.version) FROM resource_versions x WHERE x.resource_id = v.resource_id) - ?
		  AND v.version <> r.version
		  AND NOT EXISTS (SELECT 1 FROM config_ownership o WHERE o.kind = 'Resource' AND o.name = r.name AND o.applied_version = v.version)
		LIMIT ?)`},
	{"policy versions", `DELETE FROM policy_versions WHERE (name, version) IN (
		SELECT v.name, v.version FROM policy_versions v
		WHERE v.version <= (SELECT MAX(x.version) FROM policy_versions x WHERE x.name = v.name) - ?
		  AND NOT EXISTS (SELECT 1 FROM policy_deployments d WHERE d.name = v.name AND d.version = v.version)
		  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.policy = v.name AND r.version = v.version)
		  AND NOT EXISTS (SELECT 1 FROM config_ownership o WHERE o.kind = 'Policy' AND o.name = v.name AND o.applied_version = v.version)
		LIMIT ?)`},
	{"module versions", `DELETE FROM module_versions WHERE (name, version) IN (
		SELECT v.name, v.version FROM module_versions v
		WHERE v.version <= (SELECT MAX(x.version) FROM module_versions x WHERE x.name = v.name) - ?
		  AND NOT EXISTS (SELECT 1 FROM config_ownership o WHERE o.kind = 'Module' AND o.name = v.name AND o.applied_version = v.version)
		LIMIT ?)`},
}

// The operation rules delete finished entries last updated before a
// cutoff. Unresolved, running, indeterminate and awaiting entries are kept
// however old they are.
var operationRules = []rule{
	{"resource operations", `DELETE FROM resource_operations WHERE id IN (
		SELECT id FROM resource_operations WHERE unresolved = 0 AND updated_at < ? LIMIT ?)`},
	// A prepared operation was never applied; its preview expired long
	// before the cutoff, so it can no longer start.
	{"security host operations", `DELETE FROM security_host_operations WHERE seq IN (
		SELECT seq FROM security_host_operations
		WHERE updated_at < ? AND json_extract(operation_body, '$.state') IN ('succeeded', 'failed', 'prepared') LIMIT ?)`},
	// A test episode is its synthetic incident; deleting the incident
	// cascades to the test row and its handling. Episodes still firing or
	// waiting for delivery are kept.
	{"notification tests", `DELETE FROM security_incidents WHERE id IN (
		SELECT t.incident_id FROM security_notification_tests t JOIN security_incidents i ON i.id = t.incident_id
		WHERE t.created_at < ? AND i.state IN ('resolved', 'retired')
		  AND NOT EXISTS (SELECT 1 FROM security_outbox o WHERE o.incident_id = t.incident_id)
		LIMIT ?)`},
}

// failedLogins keeps the newest FailedLoginKeep denied logins, read in ID
// order from the index audit_log_action (action, outcome, id).
var failedLogins = rule{"failed logins", `DELETE FROM audit_log WHERE id IN (
	SELECT id FROM audit_log WHERE action = 'auth.login' AND outcome = 'denied'
	  AND id < (SELECT id FROM audit_log WHERE action = 'auth.login' AND outcome = 'denied' ORDER BY id DESC LIMIT 1 OFFSET ?)
	ORDER BY id LIMIT ?)`}

type rule struct {
	name  string
	query string
}

// Versions deletes document versions beyond the newest VersionKeep of each
// Resource, Policy and Module, except the current version of a resource,
// the deployed version of a policy, any policy version a stored run uses
// and the version config sync last applied. It returns how many versions
// it deleted.
func (p *Pruner) Versions(ctx context.Context) (int64, error) {
	return p.run(ctx, versionRules, p.cfg.VersionKeep)
}

// Operations deletes finished operations older than OperationAge. It
// returns how many entries it deleted.
func (p *Pruner) Operations(ctx context.Context) (int64, error) {
	return p.run(ctx, operationRules, p.cfg.Clock.Now().Add(-p.cfg.OperationAge).UnixMilli())
}

// FailedLogins deletes the oldest failed-login audit entries beyond
// FailedLoginKeep. Every other audit entry is kept forever. It returns how
// many entries it deleted.
func (p *Pruner) FailedLogins(ctx context.Context) (int64, error) {
	return p.run(ctx, []rule{failedLogins}, p.cfg.FailedLoginKeep-1)
}

func (p *Pruner) run(ctx context.Context, rules []rule, arg any) (int64, error) {
	var total int64
	for _, r := range rules {
		n, err := p.deleteBatches(ctx, r.query, arg)
		total += n
		if err != nil {
			return total, fmt.Errorf("retention: %s: %w", r.name, err)
		}
	}
	return total, nil
}

// deleteBatches runs query, whose last placeholder is the batch size, until
// it deletes less than a batch.
func (p *Pruner) deleteBatches(ctx context.Context, query string, arg any) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := p.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, query, arg, batch)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		total += n
		if err != nil || n < batch {
			return total, err
		}
	}
}
