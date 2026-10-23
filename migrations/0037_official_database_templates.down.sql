-- Refuse to narrow the constraint while non-PostgreSQL provision evidence is
-- present. Operators must retain or explicitly archive those rows first.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM template_provision_runs WHERE template_slug <> 'postgres'
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade official_database_templates while non-PostgreSQL provision runs exist';
    END IF;
END
$$;

ALTER TABLE template_provision_runs
    DROP CONSTRAINT IF EXISTS template_provision_runs_template_chk;

ALTER TABLE template_provision_runs
    ADD CONSTRAINT template_provision_runs_template_chk
    CHECK (template_slug = 'postgres');

DELETE FROM schema_migrations WHERE version = 37;
