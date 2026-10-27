-- Policy engine: versioned policy documents, deployment state, runs and
-- per-node checkpoints. Times are Unix milliseconds (UTC).

-- Every deploy of a changed document adds a version. modules holds the
-- canonical JSON of every module the version uses, keyed by name, so a run
-- can always be recompiled exactly as it was deployed.
CREATE TABLE policy_versions (
    name            TEXT    NOT NULL,
    version         INTEGER NOT NULL CHECK (version > 0),
    document        TEXT    NOT NULL,
    modules         TEXT    NOT NULL DEFAULT '{}',
    created_at      INTEGER NOT NULL,
    created_by_kind TEXT    NOT NULL,
    created_by_id   TEXT    NOT NULL,
    created_by_name TEXT    NOT NULL,
    PRIMARY KEY (name, version)
) WITHOUT ROWID;

-- One row per deployed policy; undeploy deletes the row and keeps versions.
CREATE TABLE policy_deployments (
    name        TEXT    PRIMARY KEY,
    version     INTEGER NOT NULL,
    paused      INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
    deployed_at INTEGER NOT NULL,
    FOREIGN KEY (name, version) REFERENCES policy_versions (name, version)
) WITHOUT ROWID;

-- Run ids are ULIDs, so ordering by id is creation order; queued events
-- are started in that order.
CREATE TABLE runs (
    id           TEXT    PRIMARY KEY,
    policy       TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    status       TEXT    NOT NULL CHECK (status IN (
                     'queued', 'running', 'suspended', 'succeeded',
                     'failed', 'cancelled', 'interrupted', 'dropped')),
    trigger_node TEXT    NOT NULL,
    event        TEXT    NOT NULL,
    mode         TEXT    NOT NULL CHECK (mode IN ('live', 'dry')),
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER,
    error        TEXT    NOT NULL DEFAULT '',
    FOREIGN KEY (policy, version) REFERENCES policy_versions (name, version)
) WITHOUT ROWID;
CREATE INDEX runs_policy_id ON runs (policy, id);
CREATE INDEX runs_active ON runs (status) WHERE status IN ('queued', 'running', 'suspended');

-- Only states that recovery or the trace needs are stored. Dead and
-- not-yet-reached nodes are derived from the plan and the completed rows.
CREATE TABLE run_nodes (
    run_id         TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    node_id        TEXT    NOT NULL,
    status         TEXT    NOT NULL CHECK (status IN (
                       'running', 'retrying', 'suspended', 'resumed',
                       'completed', 'failed')),
    port           TEXT    NOT NULL DEFAULT '',
    props          TEXT,
    output         TEXT,
    attempts       INTEGER NOT NULL DEFAULT 0,
    error          TEXT    NOT NULL DEFAULT '',
    started_at     INTEGER,
    finished_at    INTEGER,
    suspend_key    TEXT    NOT NULL DEFAULT '',
    resume_at      INTEGER,
    resume_payload TEXT,
    PRIMARY KEY (run_id, node_id)
) WITHOUT ROWID;
