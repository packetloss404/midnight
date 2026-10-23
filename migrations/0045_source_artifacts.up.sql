-- 0045_source_artifacts.up.sql
--
-- Durable metadata and explicit ownership for content-addressed tenant source
-- archives. Filesystem blobs are immutable and digest-derived; these rows
-- identify active owners and released references for conservative age-gated
-- garbage collection.

CREATE TABLE source_artifacts (
    digest TEXT PRIMARY KEY,
    compressed_bytes BIGINT NOT NULL,
    uncompressed_bytes BIGINT NOT NULL,
    entry_count INTEGER NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT source_artifacts_digest_chk
        CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT source_artifacts_sizes_chk
        CHECK (
            compressed_bytes > 0
            AND uncompressed_bytes >= 0
            AND entry_count > 0
        ),
    CONSTRAINT source_artifacts_storage_key_chk
        CHECK (
            storage_key ~ '^blobs/sha256/[0-9a-f]{2}/[0-9a-f]{64}[.]tar[.]gz$'
            AND storage_key = (
                'blobs/sha256/'
                || substring(digest FROM 8 FOR 2)
                || '/'
                || substring(digest FROM 8)
                || '.tar.gz'
            )
    )
);

-- One composite key is the durable source-of-truth for artifact ownership.
-- `id` is already unique, but the wider constraint lets the reference prove
-- that its operation and complete tenant tuple are the exact values persisted
-- on the named canonical deployment. This prevents otherwise-valid IDs from
-- different lineages being mixed in one ownership row.
ALTER TABLE deployments
    ADD CONSTRAINT deployments_source_artifact_lineage_uq
    UNIQUE (id, operation_id, project_id, service_id, environment_id);

CREATE TABLE source_artifact_refs (
    operation_id TEXT PRIMARY KEY
        CONSTRAINT source_artifact_refs_operation_lifecycle_fk
        REFERENCES operations(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL UNIQUE,
    artifact_digest TEXT NOT NULL
        REFERENCES source_artifacts(digest) ON DELETE RESTRICT,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at TIMESTAMPTZ,
    CONSTRAINT source_artifact_refs_deployment_lineage_fk
        FOREIGN KEY (
            deployment_id,
            operation_id,
            project_id,
            service_id,
            environment_id
        )
        REFERENCES deployments (
            id,
            operation_id,
            project_id,
            service_id,
            environment_id
        )
        ON DELETE CASCADE
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT source_artifact_refs_message_chk
        CHECK (
            message IS NULL
            OR (
                octet_length(message) <= 256
                AND message !~ '[[:cntrl:]]'
            )
        ),
    CONSTRAINT source_artifact_refs_release_chk
        CHECK (released_at IS NULL OR released_at >= created_at)
);

CREATE INDEX source_artifact_refs_active_digest_idx
    ON source_artifact_refs (artifact_digest)
    WHERE released_at IS NULL;

CREATE INDEX source_artifact_refs_released_idx
    ON source_artifact_refs (released_at, artifact_digest)
    WHERE released_at IS NOT NULL;

CREATE INDEX source_artifacts_created_at_idx
    ON source_artifacts (created_at, digest);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (45, 'source_artifacts', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
