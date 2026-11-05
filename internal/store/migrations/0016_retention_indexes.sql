-- Indexes for bounded retention and filtered history reads. Only indexes
-- are added; no row changes.

-- The timeline of one resource, newest first by occurrence
-- (resource.EventQuery with Newest).
CREATE INDEX resource_events_occurred ON resource_events (resource_id, occurred_at, id);
-- Age-based event retention compares the ingestion time.
CREATE INDEX resource_events_ingested ON resource_events (ingested_at);

-- Finished operations expire by age; unresolved ones are never pruned.
CREATE INDEX resource_operations_finished ON resource_operations (updated_at) WHERE unresolved = 0;

-- Policy version retention keeps every version a run still references.
CREATE INDEX runs_policy_version ON runs (policy, version);

-- Audit filters, each read newest first. The action index also serves the
-- failed-login cap (action 'auth.login', outcome 'denied') in ID order.
CREATE INDEX audit_log_actor ON audit_log (actor_name, id);
CREATE INDEX audit_log_action ON audit_log (action, outcome, id);
CREATE INDEX audit_log_target ON audit_log (target, id);

-- Finished notification test episodes expire by age.
CREATE INDEX security_notification_tests_created ON security_notification_tests (created_at);
