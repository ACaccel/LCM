-- Root state is authoritative; these rows preserve administrator intent and audit history.
CREATE TABLE security_host_operations (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 action TEXT NOT NULL,
 actor TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 request_body TEXT NOT NULL,
 preview_body TEXT NOT NULL,
 operation_body TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE INDEX security_host_operations_time ON security_host_operations(updated_at,seq);
