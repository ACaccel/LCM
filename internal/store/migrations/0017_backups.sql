-- Typed backups of managed services and of LCM's own database. One row per
-- backup set, per restore and per pre-restore copy. Rows follow the
-- resource name, like deployments, and are written before the deploy helper
-- is called so that a restart of LCM finds the operation it waited for.
-- Times are Unix milliseconds (UTC).

CREATE TABLE backups (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    resource        TEXT    NOT NULL,
    -- backup: a set made by the backup action; pre-restore: the copy a
    -- restore takes of what it replaces; self: a backup of LCM's own
    -- database; restore: the restore operation itself, whose backup_id is
    -- the set it restored.
    kind            TEXT    NOT NULL CHECK (kind IN ('backup', 'pre-restore', 'self', 'restore')),
    backup_id       TEXT    NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    outcome         TEXT    NOT NULL CHECK (outcome IN ('running', 'succeeded', 'failed', 'interrupted')),
    size            INTEGER NOT NULL DEFAULT 0,
    sources         TEXT    NOT NULL DEFAULT '[]',
    manifest_sha256 TEXT    NOT NULL DEFAULT '',
    skipped         INTEGER NOT NULL DEFAULT 0,
    operation_id    TEXT    NOT NULL DEFAULT '',
    actor_kind      TEXT    NOT NULL,
    actor_id        TEXT    NOT NULL,
    actor_name      TEXT    NOT NULL,
    run_id          TEXT    NOT NULL DEFAULT '',
    error           TEXT    NOT NULL DEFAULT '',
    -- Set when retention removed the set from disk.
    pruned_at       INTEGER
);
CREATE INDEX backups_resource ON backups (resource, id);
CREATE INDEX backups_running ON backups (outcome) WHERE outcome = 'running';
-- A set exists once per resource; a restore row names the set it restored.
CREATE UNIQUE INDEX backups_set ON backups (resource, backup_id) WHERE backup_id <> '' AND kind <> 'restore';
-- Rows of removed sets and of failed operations expire by age.
CREATE INDEX backups_expiry ON backups (finished_at) WHERE outcome <> 'running';
