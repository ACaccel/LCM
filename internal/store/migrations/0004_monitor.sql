-- Health monitoring: current check and service state, raw check results,
-- one-minute rollups and external alerts. Times are Unix milliseconds
-- (UTC); latencies are microseconds. A check id is "<service>/<check>".

-- Current state per check. It lets a restart resume without emitting
-- spurious status changes.
CREATE TABLE monitor_checks (
    check_id         TEXT    PRIMARY KEY,
    service          TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    type             TEXT    NOT NULL,
    critical         INTEGER NOT NULL CHECK (critical IN (0, 1)),
    interval_ms      INTEGER NOT NULL,
    status           TEXT    NOT NULL CHECK (status IN ('unknown', 'up', 'down')),
    since_ms         INTEGER NOT NULL,
    consecutive_ok   INTEGER NOT NULL DEFAULT 0,
    consecutive_fail INTEGER NOT NULL DEFAULT 0,
    last_at          INTEGER,
    last_ok          INTEGER,
    last_latency_us  INTEGER,
    last_message     TEXT    NOT NULL DEFAULT '',
    last_beat_at     INTEGER
) WITHOUT ROWID;

CREATE INDEX monitor_checks_service ON monitor_checks (service);

CREATE TABLE monitor_services (
    service    TEXT    PRIMARY KEY,
    health     TEXT    NOT NULL CHECK (health IN ('unknown', 'healthy', 'degraded', 'down')),
    since_ms   INTEGER NOT NULL,
    labels     TEXT    NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Raw results; the id keeps several results in the same millisecond apart.
CREATE TABLE monitor_results (
    id         INTEGER PRIMARY KEY,
    check_id   TEXT    NOT NULL,
    at         INTEGER NOT NULL,
    ok         INTEGER NOT NULL CHECK (ok IN (0, 1)),
    latency_us INTEGER NOT NULL DEFAULT 0,
    message    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX monitor_results_check ON monitor_results (check_id, at);
CREATE INDEX monitor_results_at ON monitor_results (at);

-- One row per check and minute. Latency statistics cover successful runs
-- only (count - failures samples); lat_min_us and lat_max_us are NULL when
-- every run in the bucket failed.
CREATE TABLE monitor_rollups (
    check_id   TEXT    NOT NULL,
    bucket_ms  INTEGER NOT NULL,
    count      INTEGER NOT NULL,
    failures   INTEGER NOT NULL,
    lat_sum_us INTEGER NOT NULL DEFAULT 0,
    lat_min_us INTEGER,
    lat_max_us INTEGER,
    PRIMARY KEY (check_id, bucket_ms)
) WITHOUT ROWID;

CREATE INDEX monitor_rollups_bucket ON monitor_rollups (bucket_ms);

-- External alerts. A row with resolved_at NULL is currently firing.
CREATE TABLE monitor_alerts (
    source      TEXT    NOT NULL,
    fingerprint TEXT    NOT NULL,
    service     TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL,
    severity    TEXT    NOT NULL,
    labels      TEXT    NOT NULL DEFAULT '{}',
    message     TEXT    NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    resolved_at INTEGER,
    PRIMARY KEY (source, fingerprint)
) WITHOUT ROWID;

CREATE INDEX monitor_alerts_service ON monitor_alerts (service, resolved_at);
