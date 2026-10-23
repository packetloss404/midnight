-- 0044_operation_deployment_lineage.up.sql
--
-- Human operation reads are authorized through the one canonical deployment
-- that owns the operation. Free-form operation events are telemetry, not
-- authority. Make that lineage structurally unambiguous before admitting the
-- read path.
--
-- The explicit audit produces an actionable failure before the unique index is
-- attempted. No duplicate is deleted or reassigned automatically: an operator
-- must investigate and repair ambiguous lineage. The table lock keeps the
-- audit and constraint installation in one stable write boundary.

LOCK TABLE deployments IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    duplicate_operation_count BIGINT;
    duplicate_operation_sample TEXT;
BEGIN
    SELECT COUNT(*)
    INTO duplicate_operation_count
    FROM (
        SELECT operation_id
        FROM deployments
        WHERE operation_id IS NOT NULL
        GROUP BY operation_id
        HAVING COUNT(*) > 1
    ) duplicates;

    IF duplicate_operation_count > 0 THEN
        SELECT COALESCE(string_agg(operation_id, ', ' ORDER BY operation_id), '')
        INTO duplicate_operation_sample
        FROM (
            SELECT operation_id
            FROM deployments
            WHERE operation_id IS NOT NULL
            GROUP BY operation_id
            HAVING COUNT(*) > 1
            ORDER BY operation_id
            LIMIT 20
        ) sample;

        RAISE EXCEPTION
            'operation deployment lineage audit found % duplicate operation_id value(s); repair before retrying (sample: %)',
            duplicate_operation_count,
            duplicate_operation_sample;
    END IF;
END
$$;

CREATE UNIQUE INDEX deployments_operation_id_uniq
    ON deployments (operation_id)
    WHERE operation_id IS NOT NULL;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (44, 'operation_deployment_lineage', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
