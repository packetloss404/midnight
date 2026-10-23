-- 0010_agent_conversations.down.sql — drop AI agent chat tables.

DROP INDEX IF EXISTS idx_agent_messages_created_at;
DROP INDEX IF EXISTS idx_agent_messages_conversation_seq;
DROP TABLE IF EXISTS agent_messages;

DROP INDEX IF EXISTS idx_agent_conversations_updated_at;
DROP INDEX IF EXISTS idx_agent_conversations_actor;
DROP INDEX IF EXISTS idx_agent_conversations_service;
DROP INDEX IF EXISTS idx_agent_conversations_project;
DROP TABLE IF EXISTS agent_conversations;

DELETE FROM schema_migrations WHERE version = 10;
