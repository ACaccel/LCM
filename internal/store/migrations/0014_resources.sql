-- One resource registry for services, the local machine and network devices.
-- Machines keep their IDs; services get new IDs in the Go part of this
-- migration (migrate_resources.go), which also converts every stored
-- document to lcm/v2 and drops the service tables. Name-keyed history
-- (timeline, deployments, checks) follows the name; observations,
-- operations and maintenance follow the resource ID.

-- Parent and child tables are rebuilt in one transaction; constraints are
-- checked once at commit.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE resources (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    type       TEXT    NOT NULL,
    adapter    TEXT    NOT NULL,
    phase      TEXT    NOT NULL CHECK (phase IN ('managed', 'retired', 'unmanaged')),
    version    INTEGER NOT NULL CHECK (version > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- Unmanaged resources are archives: they release their name.
CREATE UNIQUE INDEX resources_active_name ON resources (name) WHERE phase != 'unmanaged';
CREATE UNIQUE INDEX resources_local_machine ON resources (type, adapter)
    WHERE type = 'machine' AND adapter = 'local' AND phase != 'unmanaged';

-- Versions belong to one incarnation (ID); re-creating a name starts at 1.
CREATE TABLE resource_versions (
    resource_id   TEXT    NOT NULL REFERENCES resources (id),
    version       INTEGER NOT NULL CHECK (version > 0),
    document      TEXT    NOT NULL,
    restored_from INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    actor_kind    TEXT    NOT NULL,
    actor_id      TEXT    NOT NULL,
    actor_name    TEXT    NOT NULL,
    PRIMARY KEY (resource_id, version)
) WITHOUT ROWID;

-- Lifecycle state of service resources. Retirement is a phase, not a state.
CREATE TABLE service_states (
    resource_id TEXT    PRIMARY KEY REFERENCES resources (id),
    state       TEXT    NOT NULL CHECK (state IN (
                    'registered', 'deploying', 'running', 'updating',
                    'rolling_back', 'stopped', 'failed')),
    state_since INTEGER NOT NULL
) WITHOUT ROWID;

-- Latest observed status of observed resources (machines, routers).
CREATE TABLE resource_status (
    resource_id   TEXT    PRIMARY KEY REFERENCES resources (id),
    identity_hash TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT '{}',
    updated_at    INTEGER NOT NULL
) WITHOUT ROWID;

-- Expressions the conversion could not rewrite, for the startup log.
CREATE TABLE migration_warnings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    migration  INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    version    INTEGER NOT NULL DEFAULT 0,
    message    TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

INSERT INTO resources (id, name, type, adapter, phase, version, created_at, updated_at)
SELECT id, name, 'machine', 'local', phase, version, created_at, updated_at FROM machines;

-- Documents are converted to lcm/v2 by the Go part.
INSERT INTO resource_versions (resource_id, version, document, restored_from, created_at, actor_kind, actor_id, actor_name)
SELECT machine_id, version, document, restored_from, created_at,
       COALESCE(json_extract(actor, '$.kind'), 'system'),
       COALESCE(json_extract(actor, '$.id'), ''),
       COALESCE(json_extract(actor, '$.name'), '')
FROM machine_versions;

INSERT INTO resource_status (resource_id, identity_hash, status, updated_at)
SELECT id, identity_hash, status, updated_at FROM machines;

CREATE TABLE resource_snapshots (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    observed_at INTEGER NOT NULL,
    body        TEXT    NOT NULL
);
INSERT INTO resource_snapshots (id, resource_id, observed_at, body)
SELECT id, machine_id, observed_at, body FROM machine_snapshots;
CREATE INDEX resource_snapshots_time ON resource_snapshots (resource_id, observed_at, id);

CREATE TABLE resource_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    source      TEXT    NOT NULL,
    source_key  TEXT    NOT NULL,
    occurred_at INTEGER NOT NULL,
    ingested_at INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    UNIQUE (resource_id, source, source_key)
);
INSERT INTO resource_events (id, resource_id, source, source_key, occurred_at, ingested_at, kind, body)
SELECT id, machine_id, source, source_key, occurred_at, ingested_at, kind, body FROM machine_events;
CREATE INDEX resource_events_time ON resource_events (resource_id, id);

-- Minute points retain the last observation and mean of valid CPU samples.
CREATE TABLE resource_points (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    bucket_at   INTEGER NOT NULL,
    boot_id     TEXT    NOT NULL,
    samples     INTEGER NOT NULL,
    cpu_sum     REAL    NOT NULL,
    cpu_count   INTEGER NOT NULL,
    body        TEXT    NOT NULL,
    UNIQUE (resource_id, bucket_at, boot_id)
);
INSERT INTO resource_points (id, resource_id, bucket_at, boot_id, samples, cpu_sum, cpu_count, body)
SELECT id, machine_id, bucket_at, boot_id, samples, cpu_sum, cpu_count, body FROM machine_rollups;
CREATE INDEX resource_points_time ON resource_points (resource_id, bucket_at, id);

-- Clients page events and points by ID, so the counters continue where the
-- old tables stopped instead of reusing IDs of trimmed rows.
INSERT INTO sqlite_sequence (name, seq)
SELECT n.new, s.seq FROM sqlite_sequence s
JOIN (SELECT 'machine_snapshots' AS old, 'resource_snapshots' AS new
      UNION ALL SELECT 'machine_events', 'resource_events'
      UNION ALL SELECT 'machine_rollups', 'resource_points') n ON n.old = s.name
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence x WHERE x.name = n.new);
UPDATE sqlite_sequence SET seq = MAX(seq, COALESCE((SELECT s.seq FROM sqlite_sequence s WHERE s.name = CASE sqlite_sequence.name
        WHEN 'resource_snapshots' THEN 'machine_snapshots'
        WHEN 'resource_events' THEN 'machine_events'
        ELSE 'machine_rollups' END), 0))
WHERE name IN ('resource_snapshots', 'resource_events', 'resource_points');

CREATE TABLE resource_operations (
    id              TEXT    PRIMARY KEY,
    resource_id     TEXT    NOT NULL REFERENCES resources (id),
    idempotency_key TEXT    NOT NULL,
    request_hash    TEXT    NOT NULL,
    action          TEXT    NOT NULL,
    state           TEXT    NOT NULL,
    unresolved      INTEGER NOT NULL DEFAULT 1 CHECK (unresolved IN (0, 1)),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    body            TEXT    NOT NULL,
    UNIQUE (resource_id, idempotency_key)
);
INSERT INTO resource_operations (id, resource_id, idempotency_key, request_hash, action, state, unresolved, created_at, updated_at, body)
SELECT id, machine_id, idempotency_key, request_hash, action, state, unresolved, created_at, updated_at, body FROM machine_operations;
CREATE UNIQUE INDEX resource_operations_unresolved ON resource_operations (resource_id) WHERE unresolved = 1;

ALTER TABLE machine_cursors RENAME TO machine_cursors_v1;
CREATE TABLE machine_cursors (
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    source      TEXT    NOT NULL,
    cursor      TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (resource_id, source)
);
INSERT INTO machine_cursors (resource_id, source, cursor, updated_at)
SELECT machine_id, source, cursor, updated_at FROM machine_cursors_v1;

ALTER TABLE machine_maintenance RENAME TO machine_maintenance_v1;
CREATE TABLE machine_maintenance (
    host        TEXT    PRIMARY KEY CHECK (host = 'local'),
    resource_id TEXT    NOT NULL REFERENCES resources (id),
    state       TEXT    NOT NULL,
    entered_at  INTEGER NOT NULL,
    body        TEXT    NOT NULL
);
INSERT INTO machine_maintenance (host, resource_id, state, entered_at, body)
SELECT host, machine_id, state, entered_at, body FROM machine_maintenance_v1;

-- Stored bodies name the resource ID like the columns do. SQLite keeps
-- number literals as written, so measurements in operation evidence are
-- not reformatted.
UPDATE resource_events SET body = json_set(json_remove(body, '$.machineId'), '$.resourceId', json_extract(body, '$.machineId'))
WHERE json_type(body, '$.machineId') IS NOT NULL;
UPDATE resource_operations SET body = json_set(json_remove(body, '$.machineId'), '$.resourceId', json_extract(body, '$.machineId'))
WHERE json_type(body, '$.machineId') IS NOT NULL;
UPDATE machine_maintenance SET body = json_set(json_remove(body, '$.machineId'), '$.resourceId', json_extract(body, '$.machineId'))
WHERE json_type(body, '$.machineId') IS NOT NULL;

DROP TABLE machine_snapshots;
DROP TABLE machine_events;
DROP TABLE machine_rollups;
DROP TABLE machine_operations;
DROP TABLE machine_cursors_v1;
DROP TABLE machine_maintenance_v1;
DROP TABLE machine_versions;
DROP TABLE machines;

-- Name-keyed history follows the resource name.
ALTER TABLE timeline RENAME COLUMN service TO resource;
DROP INDEX timeline_service;
CREATE INDEX timeline_resource ON timeline (resource, id);

ALTER TABLE deployments RENAME COLUMN service TO resource;
DROP INDEX deployments_service;
CREATE INDEX deployments_resource ON deployments (resource, id);

ALTER TABLE action_completions RENAME COLUMN service TO resource;

ALTER TABLE monitor_checks RENAME COLUMN service TO resource;
DROP INDEX monitor_checks_service;
CREATE INDEX monitor_checks_resource ON monitor_checks (resource);

ALTER TABLE monitor_services RENAME TO monitor_resources;
ALTER TABLE monitor_resources RENAME COLUMN service TO resource;

ALTER TABLE monitor_alerts RENAME COLUMN service TO resource;
DROP INDEX monitor_alerts_service;
CREATE INDEX monitor_alerts_resource ON monitor_alerts (resource, resolved_at);

UPDATE locks SET name = 'resource:' || substr(name, 9) WHERE substr(name, 1, 8) = 'service:';

-- The Go part copies the rows with converted documents and kinds, then
-- drops the old table.
ALTER TABLE config_ownership RENAME TO config_ownership_v1;
DROP INDEX config_ownership_source;
CREATE TABLE config_ownership (
    kind            TEXT    NOT NULL CHECK (kind IN ('Module', 'Policy', 'Resource')),
    name            TEXT    NOT NULL,
    source          TEXT    NOT NULL,
    path            TEXT    NOT NULL,
    commit_id       TEXT    NOT NULL,
    applied_version INTEGER NOT NULL,
    document        TEXT    NOT NULL,
    pending         TEXT    NOT NULL DEFAULT '',
    pending_path    TEXT    NOT NULL DEFAULT '',
    conflict_commit TEXT    NOT NULL DEFAULT '',
    acknowledged    INTEGER NOT NULL DEFAULT 0 CHECK (acknowledged IN (0, 1)),
    orphaned        INTEGER NOT NULL DEFAULT 0 CHECK (orphaned IN (0, 1)),
    applied_at      INTEGER NOT NULL,
    PRIMARY KEY (kind, name)
) WITHOUT ROWID;
CREATE INDEX config_ownership_source ON config_ownership (source);
