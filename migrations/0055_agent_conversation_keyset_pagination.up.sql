-- 0055 — index-backed agent conversation keyset pagination.
--
-- Conversation pages use the immutable (created_at, id) tuple in descending
-- order. The API supports equality filters for project, service, and actor;
-- these indexes keep every supported filter combination bounded by LIMIT n+1.
CREATE INDEX IF NOT EXISTS idx_agent_conversations_created_id_desc
    ON agent_conversations (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_project_created_id_desc
    ON agent_conversations (project_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_service_created_id_desc
    ON agent_conversations (service_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_actor_created_id_desc
    ON agent_conversations (actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_project_service_created_id_desc
    ON agent_conversations (project_id, service_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_project_actor_created_id_desc
    ON agent_conversations (project_id, actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_service_actor_created_id_desc
    ON agent_conversations (service_id, actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_project_service_actor_created_id_desc
    ON agent_conversations (project_id, service_id, actor_id, created_at DESC, id DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (55, 'agent_conversation_keyset_pagination', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
