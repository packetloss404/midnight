-- 0062 — bounded latest-first project audit browsing.

CREATE INDEX audit_logs_project_id_desc_idx
    ON audit_logs (project_id, id DESC)
    WHERE project_id IS NOT NULL;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (62, 'project_audit_pagination', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
