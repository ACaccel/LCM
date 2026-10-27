-- Triggers, policy state and named locks. Times are Unix milliseconds (UTC).

-- Every write to policy_state takes the next value of state_seq as the
-- row's version, so a version is never reused, even after a delete and a
-- re-create, and compare-and-set on versions cannot suffer from ABA.
CREATE TABLE state_seq (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    n  INTEGER NOT NULL
);
INSERT INTO state_seq (id, n) VALUES (1, 0);

CREATE TABLE policy_state (
    policy     TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (policy, key)
) WITHOUT ROWID;

-- One row per name. A row whose expires_at has passed is free: the next
-- acquirer overwrites it, so no background cleanup is needed for safety.
CREATE TABLE locks (
    name        TEXT    PRIMARY KEY,
    owner       TEXT    NOT NULL,
    acquired_at INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX locks_owner ON locks (owner);

-- Progress of trigger watchers, such as the last commit seen by trigger.git
-- or the last fire time of trigger.cron, so restarts neither re-fire nor
-- lose events. data is a JSON object owned by the trigger type.
CREATE TABLE trigger_state (
    policy     TEXT    NOT NULL,
    node_id    TEXT    NOT NULL,
    data       TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (policy, node_id)
) WITHOUT ROWID;
