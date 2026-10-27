-- Policy-as-code: registered config sources and the ownership of resources
-- that were applied from them. Times are Unix milliseconds (UTC).

CREATE TABLE config_sources (
    name         TEXT    PRIMARY KEY,
    repo         TEXT    NOT NULL,
    branch       TEXT    NOT NULL,
    path         TEXT    NOT NULL,
    interval_ms  INTEGER NOT NULL CHECK (interval_ms > 0),
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    -- Last commit that was read and applied; empty before the first sync.
    last_commit  TEXT    NOT NULL DEFAULT '',
    last_sync_at INTEGER,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) WITHOUT ROWID;

-- One row per resource that a source manages. Drift is not stored: it is
-- computed by comparing the resource's current document with `document`.
-- There is deliberately no foreign key to config_sources; deleting a source
-- releases its rows in the same transaction.
CREATE TABLE config_ownership (
    kind            TEXT    NOT NULL CHECK (kind IN ('Module', 'Service', 'Policy')),
    name            TEXT    NOT NULL,
    source          TEXT    NOT NULL,
    path            TEXT    NOT NULL,
    commit_id       TEXT    NOT NULL,
    -- Resource version that LCM wrote (or last found equal to the repository).
    applied_version INTEGER NOT NULL,
    -- Canonical JSON of the repository document LCM last accounted for.
    document        TEXT    NOT NULL,
    -- A newer repository document that was skipped because the resource had
    -- drifted, with the commit and file it came from.
    pending         TEXT    NOT NULL DEFAULT '',
    pending_path    TEXT    NOT NULL DEFAULT '',
    conflict_commit TEXT    NOT NULL DEFAULT '',
    acknowledged    INTEGER NOT NULL DEFAULT 0 CHECK (acknowledged IN (0, 1)),
    orphaned        INTEGER NOT NULL DEFAULT 0 CHECK (orphaned IN (0, 1)),
    applied_at      INTEGER NOT NULL,
    PRIMARY KEY (kind, name)
) WITHOUT ROWID;
CREATE INDEX config_ownership_source ON config_ownership (source);
