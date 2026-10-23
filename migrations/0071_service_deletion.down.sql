-- 0071_service_deletion.down.sql — restore the pre-0071 slug uniqueness.
--
-- The pre-0071 schema cannot represent a deleted service that shares a slug
-- with a live one, so those rows are renamed rather than deleted: renaming
-- keeps every deployment, audit, and usage row reachable, while dropping the
-- rows would cascade through the whole service subtree.

DROP INDEX IF EXISTS idx_services_project_live;

DROP INDEX IF EXISTS deployments_service_live_intent_idx;

UPDATE services a
   SET slug = a.slug || '-deleted-' || a.id
 WHERE a.deleted_at IS NOT NULL
   AND EXISTS (
       SELECT 1
         FROM services b
        WHERE b.project_id = a.project_id
          AND b.slug = a.slug
          AND b.id <> a.id
          AND (b.deleted_at IS NULL OR b.id < a.id)
   );

DROP INDEX IF EXISTS services_project_slug_live_uq;

ALTER TABLE services
    ADD CONSTRAINT services_project_id_slug_key UNIQUE (project_id, slug);

DELETE FROM schema_migrations
 WHERE version = 71
   AND name = 'service_deletion';
