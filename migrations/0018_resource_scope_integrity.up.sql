-- 0018_resource_scope_integrity.up.sql
--
-- Closes the project/service/environment "mix and match" gap for durable
-- mutation rows. Existing data is handled conservatively:
--
--   * rows whose service and environment already agree on a canonical project
--     are snapshotted and repaired to that project;
--   * ambiguous rows are snapshotted in resource_scope_quarantine and retained;
--   * composite foreign keys are installed NOT VALID. PostgreSQL enforces them
--     for every new/updated row immediately without rejecting the migration for
--     an ambiguous legacy row that still needs operator review.
--
-- No user row is deleted by this migration.

CREATE TABLE IF NOT EXISTS resource_scope_quarantine (
    source_table TEXT NOT NULL,
    source_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    row_data JSONB NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ,
    PRIMARY KEY (source_table, source_id, reason)
);

-- Snapshot deterministic repairs before changing their project_id.
INSERT INTO resource_scope_quarantine (
    source_table, source_id, reason, row_data, resolved_at
)
SELECT 'deployments', d.id, 'project_id_repaired', to_jsonb(d), CURRENT_TIMESTAMP
FROM deployments d
JOIN services s ON s.id = d.service_id
JOIN environments e ON e.id = d.environment_id
WHERE s.project_id IS NOT NULL
  AND s.project_id = e.project_id
  AND d.project_id IS DISTINCT FROM s.project_id
ON CONFLICT DO NOTHING;

UPDATE deployments d
SET project_id = s.project_id,
    updated_at = CURRENT_TIMESTAMP,
    resource_version = d.resource_version + 1
FROM services s, environments e
WHERE s.id = d.service_id
  AND e.id = d.environment_id
  AND s.project_id IS NOT NULL
  AND s.project_id = e.project_id
  AND d.project_id IS DISTINCT FROM s.project_id;

INSERT INTO resource_scope_quarantine (
    source_table, source_id, reason, row_data, resolved_at
)
SELECT 'volumes', v.id, 'project_id_repaired', to_jsonb(v), CURRENT_TIMESTAMP
FROM volumes v
JOIN services s ON s.id = v.service_id
JOIN environments e ON e.id = v.environment_id
WHERE s.project_id IS NOT NULL
  AND s.project_id = e.project_id
  AND v.project_id IS DISTINCT FROM s.project_id
ON CONFLICT DO NOTHING;

UPDATE volumes v
SET project_id = s.project_id,
    updated_at = CURRENT_TIMESTAMP,
    resource_version = v.resource_version + 1
FROM services s, environments e
WHERE s.id = v.service_id
  AND e.id = v.environment_id
  AND s.project_id IS NOT NULL
  AND s.project_id = e.project_id
  AND v.project_id IS DISTINCT FROM s.project_id;

INSERT INTO resource_scope_quarantine (
    source_table, source_id, reason, row_data, resolved_at
)
SELECT 'service_variables', v.id, 'project_id_repaired', to_jsonb(v), CURRENT_TIMESTAMP
FROM service_variables v
JOIN environments e ON e.id = v.environment_id
LEFT JOIN services s ON s.id = NULLIF(v.service_id, '')
WHERE v.project_id IS DISTINCT FROM e.project_id
  AND (v.service_id = '' OR s.project_id = e.project_id)
ON CONFLICT DO NOTHING;

UPDATE service_variables v
SET project_id = e.project_id,
    updated_at = CURRENT_TIMESTAMP,
    version = v.version + 1
FROM environments e
WHERE e.id = v.environment_id
  AND v.project_id IS DISTINCT FROM e.project_id
  AND (
      v.service_id = ''
      OR EXISTS (
          SELECT 1
          FROM services s
          WHERE s.id = v.service_id
            AND s.project_id = e.project_id
      )
  );

INSERT INTO resource_scope_quarantine (
    source_table, source_id, reason, row_data, resolved_at
)
SELECT 'artifact_promotions', p.id::TEXT, 'project_id_repaired', to_jsonb(p), CURRENT_TIMESTAMP
FROM artifact_promotions p
JOIN services s ON s.id = p.service_id
JOIN environments dest ON dest.id = p.dest_environment_id
LEFT JOIN environments src ON src.id = p.source_environment_id
WHERE s.project_id IS NOT NULL
  AND s.project_id = dest.project_id
  AND (p.source_environment_id IS NULL OR src.project_id = s.project_id)
  AND p.project_id IS DISTINCT FROM s.project_id
ON CONFLICT DO NOTHING;

UPDATE artifact_promotions p
SET project_id = s.project_id
FROM services s, environments dest
WHERE s.id = p.service_id
  AND dest.id = p.dest_environment_id
  AND s.project_id IS NOT NULL
  AND s.project_id = dest.project_id
  AND (
      p.source_environment_id IS NULL
      OR EXISTS (
          SELECT 1
          FROM environments src
          WHERE src.id = p.source_environment_id
            AND src.project_id = s.project_id
      )
  )
  AND p.project_id IS DISTINCT FROM s.project_id;

-- Retain and inventory every relationship that is still ambiguous.
INSERT INTO resource_scope_quarantine (source_table, source_id, reason, row_data)
SELECT 'deployments', d.id, 'cross_project_relationship', to_jsonb(d)
FROM deployments d
LEFT JOIN services s ON s.id = d.service_id
LEFT JOIN environments e ON e.id = d.environment_id
WHERE s.id IS NULL
   OR e.id IS NULL
   OR s.project_id IS NULL
   OR s.project_id IS DISTINCT FROM e.project_id
   OR d.project_id IS DISTINCT FROM s.project_id
ON CONFLICT DO NOTHING;

INSERT INTO resource_scope_quarantine (source_table, source_id, reason, row_data)
SELECT 'volumes', v.id, 'cross_project_relationship', to_jsonb(v)
FROM volumes v
LEFT JOIN services s ON s.id = v.service_id
LEFT JOIN environments e ON e.id = v.environment_id
WHERE s.id IS NULL
   OR e.id IS NULL
   OR s.project_id IS NULL
   OR s.project_id IS DISTINCT FROM e.project_id
   OR v.project_id IS DISTINCT FROM s.project_id
ON CONFLICT DO NOTHING;

INSERT INTO resource_scope_quarantine (source_table, source_id, reason, row_data)
SELECT 'service_variables', v.id, 'cross_project_relationship', to_jsonb(v)
FROM service_variables v
LEFT JOIN environments e ON e.id = v.environment_id
LEFT JOIN services s ON s.id = NULLIF(v.service_id, '')
WHERE e.id IS NULL
   OR v.project_id IS DISTINCT FROM e.project_id
   OR (
       v.service_id <> ''
       AND (
           s.id IS NULL
           OR s.project_id IS NULL
           OR s.project_id IS DISTINCT FROM e.project_id
       )
   )
ON CONFLICT DO NOTHING;

INSERT INTO resource_scope_quarantine (source_table, source_id, reason, row_data)
SELECT 'artifact_promotions', p.id::TEXT, 'cross_project_relationship', to_jsonb(p)
FROM artifact_promotions p
LEFT JOIN services s ON s.id = p.service_id
LEFT JOIN environments dest ON dest.id = p.dest_environment_id
LEFT JOIN environments src ON src.id = p.source_environment_id
WHERE s.id IS NULL
   OR dest.id IS NULL
   OR s.project_id IS NULL
   OR p.project_id IS DISTINCT FROM s.project_id
   OR dest.project_id IS DISTINCT FROM s.project_id
   OR (
       p.source_environment_id IS NOT NULL
       AND (src.id IS NULL OR src.project_id IS DISTINCT FROM s.project_id)
   )
ON CONFLICT DO NOTHING;

-- Composite parent keys used by the relationship foreign keys below.
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_id_project_scope
    ON services (id, project_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_environments_id_project_scope
    ON environments (id, project_id);

-- NOT VALID skips a legacy-table scan but still enforces every future insert
-- and update. Operators can resolve resource_scope_quarantine then VALIDATE
-- these constraints without another application release.
ALTER TABLE deployments
    ADD CONSTRAINT deployments_service_project_fk
    FOREIGN KEY (service_id, project_id)
    REFERENCES services (id, project_id)
    ON DELETE CASCADE NOT VALID;
ALTER TABLE deployments
    ADD CONSTRAINT deployments_environment_project_fk
    FOREIGN KEY (environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE volumes
    ADD CONSTRAINT volumes_service_project_fk
    FOREIGN KEY (service_id, project_id)
    REFERENCES services (id, project_id)
    ON DELETE CASCADE NOT VALID;
ALTER TABLE volumes
    ADD CONSTRAINT volumes_environment_project_fk
    FOREIGN KEY (environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE service_variables
    ADD CONSTRAINT service_variables_environment_project_fk
    FOREIGN KEY (environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE artifact_promotions
    ADD CONSTRAINT artifact_promotions_service_project_fk
    FOREIGN KEY (service_id, project_id)
    REFERENCES services (id, project_id)
    ON DELETE CASCADE NOT VALID;
ALTER TABLE artifact_promotions
    ADD CONSTRAINT artifact_promotions_dest_environment_project_fk
    FOREIGN KEY (dest_environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;
ALTER TABLE artifact_promotions
    ADD CONSTRAINT artifact_promotions_source_environment_project_fk
    FOREIGN KEY (source_environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (18, 'resource_scope_integrity', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
