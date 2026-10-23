DROP TABLE IF EXISTS instance_domain_settings;

DELETE FROM schema_migrations
 WHERE version = 72
   AND name = 'instance_domain_settings';
