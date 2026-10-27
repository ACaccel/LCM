-- API token scopes: admin tokens use the whole API except token and user
-- management; ingest tokens may only feed alerts, heartbeats and webhooks.
-- Existing tokens keep their full access.
ALTER TABLE api_tokens ADD COLUMN scope TEXT NOT NULL DEFAULT 'admin' CHECK (scope IN ('admin', 'ingest'));
