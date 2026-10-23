-- 0054 — index-backed operation collection keyset pagination.
--
-- The API orders by the immutable (created_at, id) tuple and applies optional
-- kind/state equality filters before seeking past the cursor. These indexes
-- keep every supported filter shape bounded by LIMIT n+1.
CREATE INDEX IF NOT EXISTS idx_operations_created_id
    ON operations (created_at, id);

CREATE INDEX IF NOT EXISTS idx_operations_kind_created_id
    ON operations (kind, created_at, id);

CREATE INDEX IF NOT EXISTS idx_operations_state_created_id
    ON operations (state, created_at, id);

CREATE INDEX IF NOT EXISTS idx_operations_kind_state_created_id
    ON operations (kind, state, created_at, id);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (54, 'operation_keyset_pagination', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
