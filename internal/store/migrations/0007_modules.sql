-- Versioned module documents. Policies embed the modules they use at deploy
-- time (policy_versions.modules), so editing a module never changes a
-- deployed policy until that policy is deployed again.
-- Times are Unix milliseconds (UTC).

CREATE TABLE module_versions (
    name            TEXT    NOT NULL,
    version         INTEGER NOT NULL CHECK (version > 0),
    document        TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    created_by_kind TEXT    NOT NULL,
    created_by_id   TEXT    NOT NULL,
    created_by_name TEXT    NOT NULL,
    PRIMARY KEY (name, version)
) WITHOUT ROWID;
