-- 0014_canonical_deployments.up.sql — canonical deployment records.
--
-- Product truth for a deployment moves OUT of operation-event message strings
-- (which internal/api/deployments.go currently parses: `active := route != ""`)
-- and INTO first-class rows with a DB-enforced finite state machine.
-- `operations`/`jobs` remain request tracking; `deployments` is product truth.
--
-- ULID prefix taxonomy (additions to ADR-0002):
--   * dep_  — deployments.id
--   * dev_  — deployment_events.id
--
-- Invariants (mirrored in Go by internal/deploy/contract.go):
--   * A deployment may only be INSERTed in state 'queued'.
--   * State transitions follow the FSM in validate_deployment_contract().
--   * image_ref / image_digest are immutable once set (a rebuild is a new row).
--   * At most ONE active deployment per (service_id, environment_id), enforced
--     structurally by a generated column + DEFERRABLE UNIQUE.
--   * Rollback creates a NEW row referencing a prior deployment's digest and
--     never rebuilds; the server resolves expected_active_deployment_id (CAS),
--     never the request payload.
--
-- SAFETY / STAGING: this migration only ADDS new, initially-empty tables and an
-- inert validator FUNCTION. It does NOT create the enforcing TRIGGER — that is
-- deliberately deferred to a later, human-gated migration once every
-- deployment-state writer has been audited and adapted (see
-- docs/midnightlight-consolidation.md). Applying 0014 therefore changes no
-- existing code path's behavior.
--
-- Schema bumps to schema_version = 14.

CREATE TABLE IF NOT EXISTS deployments (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id            TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id        TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    action                TEXT NOT NULL,
    source_type           TEXT NOT NULL DEFAULT '',
    repo                  TEXT NOT NULL DEFAULT '',
    ref                   TEXT NOT NULL DEFAULT '',
    sha                   TEXT NOT NULL DEFAULT '',
    root_directory        TEXT NOT NULL DEFAULT '',
    dockerfile_path       TEXT NOT NULL DEFAULT '',
    builder               TEXT NOT NULL DEFAULT '',
    image_ref             TEXT NOT NULL DEFAULT '',
    image_digest          TEXT NOT NULL DEFAULT '',
    state                 TEXT NOT NULL DEFAULT 'queued',
    active                BOOLEAN NOT NULL DEFAULT FALSE,
    -- Generated column carries the (service, env) key ONLY while active, so a
    -- single partial-unique constraint enforces "at most one active per env".
    active_environment_id TEXT GENERATED ALWAYS AS (
        CASE WHEN active THEN environment_id ELSE NULL END
    ) STORED,
    seq                   BIGINT NOT NULL DEFAULT 0,
    -- Rollback / CAS bookkeeping. rollback_source points at the prior deployment
    -- whose immutable digest we are re-pulling; expected_active is the CAS target
    -- the promoter guards against (server-resolved, never request-supplied).
    rollback_source_deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    expected_active_deployment_id TEXT,
    operation_id          TEXT REFERENCES operations(id) ON DELETE SET NULL,
    trigger_key           TEXT,
    failure_code          TEXT NOT NULL DEFAULT '',
    failure_message       TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    resource_version      BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT deployments_action_chk CHECK (action IN (
        'deploy_source','deploy_image','redeploy_rebuild_ref','redeploy_image','rollback_digest','restart_instance'
    )),
    CONSTRAINT deployments_state_chk CHECK (state IN (
        'queued','building','deploying','active','superseded','failed','canceled'
    )),
    CONSTRAINT deployments_digest_format_chk CHECK (
        image_digest = '' OR image_digest ~ '^sha256:[a-f0-9]{64}$'
    ),
    -- active flag and state must agree; terminal failure requires a code.
    CONSTRAINT deployments_active_state_chk CHECK (active = (state = 'active')),
    CONSTRAINT deployments_failed_code_chk CHECK (state <> 'failed' OR failure_code <> ''),
    CONSTRAINT deployments_trigger_key_uq UNIQUE (trigger_key)
);

-- At most one active deployment per (service, environment). DEFERRABLE so the
-- promote+supersede swap can momentarily hold two actives inside one tx.
ALTER TABLE deployments
    ADD CONSTRAINT deployments_one_active_per_service_env_uq
    UNIQUE (service_id, active_environment_id) DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX IF NOT EXISTS idx_deployments_service_env_created
    ON deployments (service_id, environment_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_deployments_operation
    ON deployments (operation_id);

-- service_environment_state owns the active pointer + optimistic-concurrency
-- version for a (service, environment). The promoter CAS-updates this in the
-- same tx that swaps deployment active flags.
CREATE TABLE IF NOT EXISTS service_environment_state (
    service_id           TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id       TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    active_deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    last_deployment_seq  BIGINT NOT NULL DEFAULT 0,
    version              BIGINT NOT NULL DEFAULT 1,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (service_id, environment_id)
);

-- Typed, append-only event ledger. Deployment status is NEVER parsed from these
-- messages; they are audit/telemetry only. seq is monotonic per deployment.
CREATE TABLE IF NOT EXISTS deployment_events (
    id             TEXT PRIMARY KEY,
    deployment_id  TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    seq            BIGINT NOT NULL,
    event_type     TEXT NOT NULL,
    step           TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT '',
    failure_code   TEXT NOT NULL DEFAULT '',
    message        TEXT NOT NULL DEFAULT '',
    data           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (deployment_id, seq),
    CONSTRAINT deployment_events_type_chk CHECK (event_type IN ('state','step','failure','log')),
    -- Belt-and-suspenders: reject obvious credential leakage at write time
    -- (app-layer redaction in internal/build is primary).
    CONSTRAINT deployment_events_no_secret_chk CHECK (
        message !~* '(authorization:\s*bearer|github_pat_|ghp_[a-z0-9]|ghs_[a-z0-9])'
    )
);

CREATE INDEX IF NOT EXISTS idx_deployment_events_deployment_seq
    ON deployment_events (deployment_id, seq);

-- FSM guard function. Rejects illegal inserts/transitions, image mutation.
-- Mirrors internal/deploy/contract.go. Defined here (inert) but NOT yet attached
-- via CREATE TRIGGER — attaching it is a human-gated follow-up migration once all
-- deployment-state writers are audited (see file header + consolidation doc).
CREATE OR REPLACE FUNCTION validate_deployment_contract() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'queued' THEN
            RAISE EXCEPTION 'deployment % must be inserted in state queued, got %', NEW.id, NEW.state;
        END IF;
        RETURN NEW;
    END IF;

    -- UPDATE: enforce the transition table.
    IF NEW.state <> OLD.state THEN
        IF NOT (
            (OLD.state = 'queued'    AND NEW.state IN ('building','deploying','failed','canceled')) OR
            (OLD.state = 'building'  AND NEW.state IN ('deploying','failed','canceled')) OR
            (OLD.state = 'deploying' AND NEW.state IN ('active','failed','canceled')) OR
            (OLD.state = 'active'    AND NEW.state = 'superseded')
        ) THEN
            RAISE EXCEPTION 'illegal deployment transition % -> % for %', OLD.state, NEW.state, NEW.id;
        END IF;
    END IF;

    -- image_ref / image_digest are immutable once set.
    IF OLD.image_ref <> '' AND NEW.image_ref <> OLD.image_ref THEN
        RAISE EXCEPTION 'deployment % image_ref is immutable', NEW.id;
    END IF;
    IF OLD.image_digest <> '' AND NEW.image_digest <> OLD.image_digest THEN
        RAISE EXCEPTION 'deployment % image_digest is immutable', NEW.id;
    END IF;

    NEW.updated_at := CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- NOTE: intentionally NO `CREATE TRIGGER trg_validate_deployment_contract` here.
-- Attaching the trigger enforces the FSM on every writer and is the gated step.

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (14, 'canonical_deployments', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
