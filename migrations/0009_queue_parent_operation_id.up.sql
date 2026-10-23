-- 0009_queue_parent_operation_id.up.sql
--
-- Adds the explicit queue -> API operation link expected by the dequeue
-- contract. Deploy intake has historically kept operation_id in the JSON
-- payload for worker compatibility; this column makes the link queryable
-- without unpacking payload metadata.

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS parent_operation_id TEXT;

UPDATE jobs AS j
SET parent_operation_id = candidate.operation_id
FROM (
    SELECT id,
           COALESCE(NULLIF(operation_id, ''), NULLIF(payload->>'operation_id', '')) AS operation_id
    FROM jobs
) AS candidate
WHERE j.id = candidate.id
  AND j.parent_operation_id IS NULL
  AND candidate.operation_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_jobs_parent_operation_id
    ON jobs (parent_operation_id)
    WHERE parent_operation_id IS NOT NULL;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (9, 'queue_parent_operation_id', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
