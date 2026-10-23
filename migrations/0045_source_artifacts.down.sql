-- Remove ownership before immutable metadata so the restrictive digest
-- foreign key is never bypassed.

DROP TABLE IF EXISTS source_artifact_refs;
DROP TABLE IF EXISTS source_artifacts;

ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_source_artifact_lineage_uq;

DELETE FROM schema_migrations
WHERE version = 45
  AND name = 'source_artifacts';
