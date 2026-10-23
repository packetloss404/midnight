-- 0003_artifact_promotions.up.sql
--
-- Adds the artifact_promotions ledger consumed by the promotion gate. One row
-- is recorded per promotion and application code treats the ledger as
-- append-only.
--
-- The product contract promotes the same immutable image digest across
-- environments by default and records source SHA, image digest, SBOM /
-- provenance, checks, approvals, actor, gate results, and target environment.

CREATE TABLE IF NOT EXISTS artifact_promotions (
    id                      BIGSERIAL PRIMARY KEY,
    project_id              TEXT NOT NULL,
    service_id              TEXT NOT NULL,
    source_environment_id   TEXT,
    dest_environment_id     TEXT NOT NULL,
    source_sha              TEXT,
    image_digest            TEXT NOT NULL,
    sbom_digest             TEXT,
    provenance_digest       TEXT,
    checks_passed           JSONB NOT NULL DEFAULT '[]'::jsonb,
    approvals               JSONB NOT NULL DEFAULT '[]'::jsonb,
    gate_result             TEXT NOT NULL CHECK (gate_result IN ('allowed','blocked')),
    gate_reason_code        TEXT,
    actor_id                TEXT NOT NULL,
    operation_id            TEXT,
    recorded_at             TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_artifact_promotions_service_dest
    ON artifact_promotions (service_id, dest_environment_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifact_promotions_image_digest
    ON artifact_promotions (image_digest);

-- artifact_promotions is INSERT-only via documented application paths.
-- A grant restriction (UPDATE/DELETE revoked from midnight_app) is
-- applied by production role setup; application code MUST go through
-- internal/promotion.Ledger.
