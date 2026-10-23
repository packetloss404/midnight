DROP TABLE IF EXISTS monitor_delivery_outbox;
DROP TABLE IF EXISTS monitor_incidents;
DROP TABLE IF EXISTS monitor_subscriptions;
DROP TABLE IF EXISTS monitor_channels;
DROP TABLE IF EXISTS monitor_definitions;
DELETE FROM schema_migrations WHERE version = 31 AND name = 'builtin_monitors';
