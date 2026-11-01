-- Minute points retain the last observation and mean of valid CPU samples.
CREATE TABLE machine_rollups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id TEXT NOT NULL REFERENCES machines(id),
    bucket_at INTEGER NOT NULL,
    boot_id TEXT NOT NULL,
    samples INTEGER NOT NULL,
    cpu_sum REAL NOT NULL,
    cpu_count INTEGER NOT NULL,
    body TEXT NOT NULL,
    UNIQUE(machine_id,bucket_at,boot_id)
);
CREATE INDEX machine_rollups_time ON machine_rollups(machine_id,bucket_at,id);
