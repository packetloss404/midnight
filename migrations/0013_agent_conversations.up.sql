-- 0010_agent_conversations.up.sql — AI agent chat persistence.
--
-- Introduces the `agent_conversations` and `agent_messages` tables that
-- back the in-product Claude agent chat panel. The schema is independent
-- of the §1.16 host-agent endpoints (those live in `hosts` /
-- `host_heartbeats` from 0004_agents).
--
-- ULID prefix taxonomy (additions to ADR-0002):
--   * conv_  — agent_conversations.id
--   * msg_   — agent_messages.id
--
-- The content / usage / metadata columns are JSONB so the message
-- content-block model (text | tool_use | tool_result) can evolve without
-- a migration. The append path holds a `pg_advisory_xact_lock` keyed on
-- hashtext('midnight_agent_conv:<conversation_id>') so concurrent
-- writers cannot interleave messages within a single conversation.
--
-- Schema bumps to schema_version = 10. (The original spec reserved 12
-- for this slice; concurrent tracks own 10/11 in the integrator's
-- branch — see docs/adr/0024-agent-chat-backend.md "Numbering".)

CREATE TABLE IF NOT EXISTS agent_conversations (
    id           TEXT PRIMARY KEY,
    project_id   TEXT REFERENCES projects(id) ON DELETE SET NULL,
    service_id   TEXT REFERENCES services(id) ON DELETE SET NULL,
    actor_id     TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    model        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT agent_conversations_id_prefix_chk
        CHECK (id LIKE 'conv\_%' ESCAPE '\')
);

CREATE INDEX IF NOT EXISTS idx_agent_conversations_project
    ON agent_conversations (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_conversations_service
    ON agent_conversations (service_id) WHERE service_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_conversations_actor
    ON agent_conversations (actor_id) WHERE actor_id <> '';
CREATE INDEX IF NOT EXISTS idx_agent_conversations_updated_at
    ON agent_conversations (updated_at DESC);

CREATE TABLE IF NOT EXISTS agent_messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES agent_conversations(id) ON DELETE CASCADE,
    seq             BIGINT NOT NULL,
    role            TEXT NOT NULL,
    content         JSONB NOT NULL DEFAULT '[]'::jsonb,
    usage           JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (conversation_id, seq),
    CONSTRAINT agent_messages_id_prefix_chk
        CHECK (id LIKE 'msg\_%' ESCAPE '\'),
    CONSTRAINT agent_messages_role_chk
        CHECK (role IN ('user', 'assistant', 'tool', 'system'))
);

CREATE INDEX IF NOT EXISTS idx_agent_messages_conversation_seq
    ON agent_messages (conversation_id, seq);
CREATE INDEX IF NOT EXISTS idx_agent_messages_created_at
    ON agent_messages (created_at);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (10, 'agent_conversations', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
