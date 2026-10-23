-- 0067 — immutable community-template provenance and project-owned drafts.

CREATE TABLE community_template_forks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    trust_class TEXT NOT NULL DEFAULT 'local',
    upstream_manifest JSONB NOT NULL,
    draft_manifest JSONB NOT NULL,
    provenance JSONB NOT NULL,
    readme TEXT NOT NULL,
    validation_findings JSONB NOT NULL DEFAULT '[]'::JSONB,
    is_valid BOOLEAN NOT NULL,
    resource_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT community_template_forks_id_chk CHECK (id ~ '^tpl_[0-9a-f]{32}$'),
    CONSTRAINT community_template_forks_trust_chk CHECK (trust_class = 'local'),
    CONSTRAINT community_template_forks_documents_chk CHECK (
        jsonb_typeof(upstream_manifest) = 'object'
        AND jsonb_typeof(draft_manifest) = 'object'
        AND jsonb_typeof(provenance) = 'object'
        AND jsonb_typeof(validation_findings) = 'array'
    ),
    CONSTRAINT community_template_forks_provenance_chk CHECK (
        provenance->>'source' ~ '^https://github[.]com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
        AND provenance->>'ref' ~ '^[0-9a-f]{40}$'
        AND provenance->>'manifest_path' = 'template.yaml'
        AND provenance->>'manifest_sha256' ~ '^[0-9a-f]{64}$'
        AND provenance->>'readme_sha256' ~ '^[0-9a-f]{64}$'
        AND provenance->>'sbom_sha256' ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT community_template_forks_readme_chk CHECK (octet_length(readme) <= 131072),
    CONSTRAINT community_template_forks_version_chk CHECK (resource_version > 0),
    CONSTRAINT community_template_forks_time_chk CHECK (updated_at >= created_at)
);

CREATE INDEX community_template_forks_project_updated_idx
    ON community_template_forks (project_id, updated_at DESC, id);

CREATE UNIQUE INDEX community_template_forks_source_uniq
    ON community_template_forks (
        project_id,
        (provenance->>'source'),
        (provenance->>'ref'),
        (provenance->>'manifest_sha256')
    );

COMMENT ON TABLE community_template_forks IS
    'Project-local editable forks with immutable upstream manifest and exact GitHub commit provenance; application is owned by the staged changeset path.';
