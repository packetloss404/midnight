-- 0051 — tenant variable scope integrity.
--
-- Variable envelopes predate durable workspace ownership. Canonical project
-- lineage is now authoritative: repair the redaction-safe workspace metadata
-- and the duplicated ownership metadata on immutable versions before adding
-- constraints. Ciphertext, wrapped DEKs, and privacy/version metadata are not
-- changed.
--
-- Shared variables retain the historical service_id = '' sentinel. A
-- generated nullable key lets PostgreSQL enforce the service/project
-- relationship for non-shared variables without inventing a synthetic service
-- or weakening the foreign key for shared scope.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM service_variables variable
        LEFT JOIN services service
          ON service.id = NULLIF(variable.service_id, '')
         AND service.project_id = variable.project_id
        WHERE variable.service_id <> ''
          AND service.id IS NULL
    ) THEN
        RAISE EXCEPTION
            'migration 0051 requires every non-shared variable to have canonical service/project lineage';
    END IF;
END
$$;

UPDATE service_variables variable
SET workspace_id = project.workspace_id
FROM projects project
WHERE project.id = variable.project_id
  AND variable.workspace_id IS DISTINCT FROM project.workspace_id;

UPDATE service_variable_versions versioned
SET workspace_id = variable.workspace_id,
    project_id = variable.project_id,
    environment_id = variable.environment_id,
    service_id = variable.service_id,
    name = variable.name
FROM service_variables variable
WHERE variable.id = versioned.variable_id
  AND (
      versioned.workspace_id IS DISTINCT FROM variable.workspace_id
      OR versioned.project_id IS DISTINCT FROM variable.project_id
      OR versioned.environment_id IS DISTINCT FROM variable.environment_id
      OR versioned.service_id IS DISTINCT FROM variable.service_id
      OR versioned.name IS DISTINCT FROM variable.name
  );

CREATE OR REPLACE FUNCTION midnight_derive_service_variable_workspace()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    SELECT project.workspace_id
      INTO STRICT NEW.workspace_id
      FROM projects project
     WHERE project.id = NEW.project_id;
    RETURN NEW;
EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION
            'service variable project lineage is unavailable'
            USING ERRCODE = '23503';
END
$$;

DROP TRIGGER IF EXISTS service_variables_derive_workspace
ON service_variables;

CREATE TRIGGER service_variables_derive_workspace
BEFORE INSERT OR UPDATE OF project_id, workspace_id
ON service_variables
FOR EACH ROW
EXECUTE FUNCTION midnight_derive_service_variable_workspace();

CREATE OR REPLACE FUNCTION midnight_derive_service_variable_version_scope()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    SELECT variable.workspace_id,
           variable.project_id,
           variable.environment_id,
           variable.service_id,
           variable.name
      INTO STRICT
           NEW.workspace_id,
           NEW.project_id,
           NEW.environment_id,
           NEW.service_id,
           NEW.name
      FROM service_variables variable
     WHERE variable.id = NEW.variable_id;
    RETURN NEW;
EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION
            'service variable version parent lineage is unavailable'
            USING ERRCODE = '23503';
END
$$;

DROP TRIGGER IF EXISTS service_variable_versions_derive_scope
ON service_variable_versions;

CREATE TRIGGER service_variable_versions_derive_scope
BEFORE INSERT OR UPDATE OF
    variable_id,
    workspace_id,
    project_id,
    environment_id,
    service_id,
    name
ON service_variable_versions
FOR EACH ROW
EXECUTE FUNCTION midnight_derive_service_variable_version_scope();

ALTER TABLE service_variables
    ADD COLUMN service_scope_id TEXT
        GENERATED ALWAYS AS (NULLIF(service_id, '')) STORED;

ALTER TABLE service_variables
    ADD CONSTRAINT service_variables_project_workspace_fk
    FOREIGN KEY (project_id, workspace_id)
    REFERENCES projects (id, workspace_id)
    ON DELETE CASCADE
    NOT VALID;

ALTER TABLE service_variables
    ADD CONSTRAINT service_variables_service_project_fk
    FOREIGN KEY (service_scope_id, project_id)
    REFERENCES services (id, project_id)
    ON DELETE CASCADE
    NOT VALID;

CREATE UNIQUE INDEX service_variables_id_ownership_uniq
    ON service_variables (
        id,
        workspace_id,
        project_id,
        environment_id,
        service_id,
        name
    );

ALTER TABLE service_variable_versions
    ADD CONSTRAINT service_variable_versions_parent_scope_fk
    FOREIGN KEY (
        variable_id,
        workspace_id,
        project_id,
        environment_id,
        service_id,
        name
    )
    REFERENCES service_variables (
        id,
        workspace_id,
        project_id,
        environment_id,
        service_id,
        name
    )
    ON DELETE CASCADE
    NOT VALID;

ALTER TABLE service_variables
    VALIDATE CONSTRAINT service_variables_project_workspace_fk;

ALTER TABLE service_variables
    VALIDATE CONSTRAINT service_variables_service_project_fk;

ALTER TABLE service_variable_versions
    VALIDATE CONSTRAINT service_variable_versions_parent_scope_fk;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (51, 'tenant_variable_scope_integrity', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
