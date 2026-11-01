-- Machines are additive; service identities and histories retain their contracts.
CREATE TABLE machines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    host TEXT NOT NULL DEFAULT 'local' CHECK (host = 'local'),
    version INTEGER NOT NULL,
    phase TEXT NOT NULL DEFAULT 'managed' CHECK (phase IN ('managed','retired','unmanaged')),
    identity_hash TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX machines_active_name ON machines(name) WHERE phase != 'unmanaged';
CREATE UNIQUE INDEX machines_active_host ON machines(host) WHERE phase != 'unmanaged';
CREATE TABLE machine_versions (
    machine_id TEXT NOT NULL REFERENCES machines(id),
    version INTEGER NOT NULL,
    document TEXT NOT NULL,
    restored_from INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    actor TEXT NOT NULL,
    PRIMARY KEY(machine_id, version)
);
CREATE TABLE machine_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    observed_at INTEGER NOT NULL,
    body TEXT NOT NULL
);
CREATE INDEX machine_snapshots_time ON machine_snapshots(machine_id, observed_at, id);
CREATE TABLE machine_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    source TEXT NOT NULL,
    source_key TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    ingested_at INTEGER NOT NULL,
    kind TEXT NOT NULL,
    body TEXT NOT NULL,
    UNIQUE(machine_id, source, source_key)
);
CREATE INDEX machine_events_time ON machine_events(machine_id,id);
CREATE TABLE machine_cursors (
    machine_id TEXT NOT NULL REFERENCES machines(id),
    source TEXT NOT NULL,
    cursor TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(machine_id,source)
);
CREATE TABLE machine_operations (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    state TEXT NOT NULL,
    unresolved INTEGER NOT NULL DEFAULT 1 CHECK (unresolved IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    body TEXT NOT NULL,
    UNIQUE(machine_id,idempotency_key)
);
CREATE UNIQUE INDEX machine_operations_unresolved ON machine_operations(machine_id) WHERE unresolved=1;
CREATE TABLE machine_maintenance (
    host TEXT PRIMARY KEY CHECK(host='local'),
    machine_id TEXT NOT NULL REFERENCES machines(id),
    state TEXT NOT NULL,
    entered_at INTEGER NOT NULL,
    body TEXT NOT NULL
);

ALTER TABLE config_ownership RENAME TO config_ownership_old;
CREATE TABLE config_ownership (
    kind TEXT NOT NULL CHECK (kind IN ('Module','Service','Policy','Machine')),
    name TEXT NOT NULL,
    source TEXT NOT NULL,
    path TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    applied_version INTEGER NOT NULL,
    document TEXT NOT NULL,
    pending TEXT NOT NULL DEFAULT '',
    pending_path TEXT NOT NULL DEFAULT '',
    conflict_commit TEXT NOT NULL DEFAULT '',
    acknowledged INTEGER NOT NULL DEFAULT 0 CHECK(acknowledged IN (0,1)),
    orphaned INTEGER NOT NULL DEFAULT 0 CHECK(orphaned IN (0,1)),
    applied_at INTEGER NOT NULL,
    PRIMARY KEY(kind,name)
) WITHOUT ROWID;
INSERT INTO config_ownership SELECT * FROM config_ownership_old;
DROP TABLE config_ownership_old;
CREATE INDEX config_ownership_source ON config_ownership(source);
