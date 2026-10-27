-- Service catalog: hosts, versioned service documents, lifecycle state,
-- deployment history, per-service timeline and idempotency records.
-- Times are Unix milliseconds (UTC).

CREATE TABLE hosts (
    name       TEXT    PRIMARY KEY,
    kind       TEXT    NOT NULL CHECK (kind IN ('local', 'ssh')),
    address    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
) WITHOUT ROWID;

INSERT INTO hosts (name, kind, created_at)
VALUES ('local', 'local', CAST(strftime('%s', 'now') AS INTEGER) * 1000);

-- Every save of a changed document adds a version. Versions outlive the
-- service row so that history and numbering survive a delete and re-create.
CREATE TABLE service_versions (
    name            TEXT    NOT NULL,
    version         INTEGER NOT NULL CHECK (version > 0),
    document        TEXT    NOT NULL,
    restored_from   INTEGER,
    created_at      INTEGER NOT NULL,
    created_by_kind TEXT    NOT NULL,
    created_by_id   TEXT    NOT NULL,
    created_by_name TEXT    NOT NULL,
    PRIMARY KEY (name, version)
) WITHOUT ROWID;

CREATE TABLE services (
    name        TEXT    PRIMARY KEY,
    host        TEXT    NOT NULL REFERENCES hosts (name),
    version     INTEGER NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN (
                    'registered', 'deploying', 'running', 'updating',
                    'rolling_back', 'stopped', 'failed', 'retired')),
    state_since INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    FOREIGN KEY (name, version) REFERENCES service_versions (name, version)
) WITHOUT ROWID;

-- Deployments are kept after the service is deleted.
CREATE TABLE deployments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    service          TEXT    NOT NULL,
    action           TEXT    NOT NULL CHECK (action IN ('deploy', 'rollback')),
    version          TEXT    NOT NULL DEFAULT '',
    previous_version TEXT    NOT NULL DEFAULT '',
    started_at       INTEGER NOT NULL,
    finished_at      INTEGER,
    outcome          TEXT    NOT NULL CHECK (outcome IN ('running', 'succeeded', 'failed', 'interrupted')),
    actor_kind       TEXT    NOT NULL,
    actor_id         TEXT    NOT NULL,
    actor_name       TEXT    NOT NULL,
    run_id           TEXT    NOT NULL DEFAULT '',
    error            TEXT    NOT NULL DEFAULT '',
    log              TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX deployments_service ON deployments (service, id);
CREATE INDEX deployments_running ON deployments (outcome) WHERE outcome = 'running';

CREATE TABLE timeline (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    service TEXT    NOT NULL,
    time    INTEGER NOT NULL,
    kind    TEXT    NOT NULL,
    summary TEXT    NOT NULL,
    data    TEXT    NOT NULL DEFAULT '{}',
    ref     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX timeline_service ON timeline (service, id);
CREATE INDEX timeline_time ON timeline (time);

-- The last completed idempotency key per service and action: a retried
-- node whose key matches returns the stored result instead of acting again.
CREATE TABLE action_completions (
    service      TEXT    NOT NULL,
    action       TEXT    NOT NULL,
    idem_key     TEXT    NOT NULL,
    completed_at INTEGER NOT NULL,
    result       TEXT    NOT NULL,
    PRIMARY KEY (service, action)
) WITHOUT ROWID;
