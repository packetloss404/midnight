-- 0013_agent_revoked_sessions.down.sql — reverse of the up.

DELETE FROM schema_migrations WHERE version = 13;

DROP INDEX IF EXISTS idx_agent_host_budget_day;
DROP TABLE IF EXISTS agent_host_budget;

DROP TABLE IF EXISTS agent_session_budgets;

DROP INDEX IF EXISTS idx_agent_revoked_sessions_revoked_at;
DROP TABLE IF EXISTS agent_revoked_sessions;
