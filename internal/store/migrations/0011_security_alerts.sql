-- Security ingestion and delivery survive process restarts independently of SSE.
CREATE TABLE security_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    digest TEXT NOT NULL,
    received_at INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    historical INTEGER NOT NULL,
    incident_id TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL
);
CREATE INDEX security_events_time ON security_events(received_at);
CREATE TABLE security_incidents (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    condition_key TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    service TEXT NOT NULL,
    source TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('firing','resolved','retired')),
    severity TEXT NOT NULL,
    summary TEXT NOT NULL,
    opened_at INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    occurrences INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    last_queued_at INTEGER NOT NULL DEFAULT 0,
    delivery TEXT NOT NULL DEFAULT 'disabled',
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX security_incident_active ON security_incidents(condition_key) WHERE state='firing';
CREATE TABLE security_conditions (
    key TEXT PRIMARY KEY,
    observed_at INTEGER NOT NULL,
    state TEXT NOT NULL,
    incident_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE security_outbox (
    incident_id TEXT PRIMARY KEY REFERENCES security_incidents(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    next_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX security_outbox_due ON security_outbox(next_at);
CREATE TABLE security_progress (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- Normal traffic measurements are not alert lifecycle events.
CREATE TABLE security_traffic (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    observation_key TEXT NOT NULL UNIQUE,
    digest TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    service TEXT NOT NULL,
    category TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    window_end INTEGER NOT NULL,
    received_at INTEGER NOT NULL,
    body TEXT NOT NULL
);
CREATE INDEX security_traffic_window ON security_traffic(window_end,id);
CREATE INDEX security_traffic_machine ON security_traffic(machine_id,id);
