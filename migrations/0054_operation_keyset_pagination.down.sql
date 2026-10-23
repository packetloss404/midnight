DROP INDEX IF EXISTS idx_operations_kind_state_created_id;
DROP INDEX IF EXISTS idx_operations_state_created_id;
DROP INDEX IF EXISTS idx_operations_kind_created_id;
DROP INDEX IF EXISTS idx_operations_created_id;

DELETE FROM schema_migrations
WHERE version = 54
  AND name = 'operation_keyset_pagination';
