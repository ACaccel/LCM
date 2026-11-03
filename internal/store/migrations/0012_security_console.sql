-- Operator handling is independent of detector and notification revisions.
CREATE TABLE security_handling (
    incident_id TEXT PRIMARY KEY REFERENCES security_incidents(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    acknowledged INTEGER NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    silence_until INTEGER,
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL
);
CREATE TABLE security_handling_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    incident_id TEXT NOT NULL REFERENCES security_incidents(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    reason TEXT NOT NULL,
    actor TEXT NOT NULL,
    at INTEGER NOT NULL,
    silence_until INTEGER
);
CREATE INDEX security_handling_history_incident ON security_handling_history(incident_id,id);
CREATE TABLE security_settings (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    version INTEGER NOT NULL,
    notifications_enabled INTEGER NOT NULL,
    cooldown_seconds INTEGER NOT NULL,
    minimum_severity TEXT NOT NULL
);
ALTER TABLE security_incidents ADD COLUMN last_sent_revision INTEGER NOT NULL DEFAULT 0;
-- Older queues have no successful-send history. Preserve recovery eligibility
-- conservatively for episodes which may already have reached the relay.
UPDATE security_incidents SET last_sent_revision=1 WHERE last_queued_at>0;
CREATE TABLE security_notification_tests (
    request_id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL UNIQUE REFERENCES security_incidents(id) ON DELETE CASCADE,
    reason TEXT NOT NULL,
    actor TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    recovered INTEGER NOT NULL DEFAULT 0
);

-- Durable operator changes survive an independent audit transport failure.
CREATE TABLE security_settings_history (
 version INTEGER PRIMARY KEY,
 actor TEXT NOT NULL,
 at INTEGER NOT NULL,
 reason TEXT NOT NULL,
 notifications_enabled INTEGER NOT NULL,
 cooldown_seconds INTEGER NOT NULL,
 minimum_severity TEXT NOT NULL
);
