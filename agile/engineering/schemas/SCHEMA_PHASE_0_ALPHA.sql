-- =====================================================================
-- Midnight control plane schema-of-record: Phase 0 Alpha
-- =====================================================================
-- Target:    PostgreSQL 15+
-- Author:    Senior Engineer 2 of 4 (engineering pass)
-- Sources:   midnight-platform-plan.md (Phase 0 Alpha tables list, the
--            Eight-Specialist Panel Locked Decisions, Variables/Secrets/
--            RBAC/Audit, Build Plane / registry retention, Deployment
--            State Machine), and the PM dossiers under agile/features/.
--
-- Conventions:
--   * Table names: plural snake_case nouns.
--   * Primary keys: app-generated ULID-prefixed text (e.g. 'svc_01HZ…').
--     PKs are never UUIDs; the API contract pins the ULID prefix taxonomy.
--   * FK columns: <singular>_id (e.g. service_id, project_id).
--   * Every mutable resource has created_at, updated_at, resource_version.
--     resource_version is a bigint that is bumped by triggers on UPDATE
--     and used as the If-Match / optimistic-concurrency token.
--   * created_at/updated_at are timestamptz NOT NULL DEFAULT now().
--   * Range partitioning (monthly, by created_at or occurred_at) is
--     declared on every high-write table mandated by the panel:
--       events, audit_logs, jobs, host_heartbeats, deployment_instances.
--   * Hot-path indexes are co-located with each table block.
--   * Table-level COMMENTs explain intent for the next engineer.
--   * No "OPEN QUESTION" is silently resolved — they are flagged inline.
-- =====================================================================

-- ---------------------------------------------------------------------
-- 0. Extensions, helpers, roles
-- ---------------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS pgcrypto;     -- gen_random_bytes() for tokens
CREATE EXTENSION IF NOT EXISTS citext;       -- for case-insensitive emails

-- pg_partman is recommended for partition lifecycle management; it is an
-- operator-installed extension, not a hard dependency. See SCHEMA_NOTES.md
-- ("Partitioning strategy") for the manual fallback worker design.

-- Roles. Grants land at the bottom of this file; create roles up front so
-- DDL that references them does not need to be re-ordered.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'midnight_app') THEN
        CREATE ROLE midnight_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'midnight_migrate') THEN
        CREATE ROLE midnight_migrate NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'midnight_audit_ro') THEN
        CREATE ROLE midnight_audit_ro NOLOGIN;
    END IF;
END$$;

-- resource_version bump trigger. Used by every mutable resource table.
CREATE OR REPLACE FUNCTION midnight_bump_resource_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    -- Monotonic per row; integer overflow is not a concern for bigint.
    NEW.resource_version := COALESCE(OLD.resource_version, 0) + 1;
    RETURN NEW;
END
$$;

-- ---------------------------------------------------------------------
-- 1. Identity, workspaces, RBAC
-- ---------------------------------------------------------------------

CREATE TABLE users (
    id               text        PRIMARY KEY,                        -- 'usr_…'
    email            citext      NOT NULL UNIQUE,
    display_name     text        NOT NULL,
    password_hash    text,                                            -- argon2id; nullable when only OIDC linked
    totp_secret_enc  bytea,                                           -- envelope-encrypted (DEK ref in attrs)
    session_generation bigint    NOT NULL DEFAULT 1,                  -- bumped on logout-everywhere
    is_disabled      boolean     NOT NULL DEFAULT false,
    last_login_at    timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER users_rv BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE users IS
    'Local human identities. OIDC linkage and SSO arrive in v1 Foundations; '
    'Phase 0 supports password+TOTP plus the bootstrap admin path. '
    'Bootstrap path auto-disables once a normal admin exists (panel: security).';

CREATE TABLE workspaces (
    id               text        PRIMARY KEY,                        -- 'ws_…' (reserved-prefix list pins this)
    slug             text        NOT NULL UNIQUE,
    name             text        NOT NULL,
    owner_user_id    text        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    -- ownership + governance metadata required by panel (governance primitives)
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,        -- owner, team, cost_center, environment_tier, data_classification, business_service, compliance_scope
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER workspaces_rv BEFORE UPDATE ON workspaces
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE workspaces IS
    'Top-level account/team boundary. ON DELETE RESTRICT on owner_user_id is '
    'deliberate: deleting the last admin requires reassigning ownership first.';

CREATE TABLE workspace_members (
    workspace_id     text        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id          text        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role             text        NOT NULL,                            -- 'owner'|'project_admin'|'developer'|'deployer'|'viewer'
    invited_by       text        REFERENCES users(id) ON DELETE SET NULL,
    invited_at       timestamptz NOT NULL DEFAULT now(),
    accepted_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT workspace_members_role_chk
        CHECK (role IN ('owner','project_admin','developer','deployer','viewer'))
);
CREATE TRIGGER workspace_members_rv BEFORE UPDATE ON workspace_members
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE workspace_members IS
    'Workspace membership + RBAC role (panel: MVP RBAC = owner/project_admin/'
    'developer/deployer/viewer). Effective access is computed from this row '
    'plus environment tier and policy decisions; this row is a UX shortcut.';

CREATE TABLE service_accounts (
    id               text        PRIMARY KEY,                        -- 'sa_…'
    workspace_id     text        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name             text        NOT NULL,
    description      text,
    is_disabled      boolean     NOT NULL DEFAULT false,
    created_by       text        REFERENCES users(id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    UNIQUE (workspace_id, name)
);
CREATE TRIGGER service_accounts_rv BEFORE UPDATE ON service_accounts
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE service_accounts IS
    'Non-human first-class identities. Tokens belong to a service account or '
    'to a user, never directly to a workspace.';

CREATE TABLE api_tokens (
    id               text        PRIMARY KEY,                        -- 'tok_…'
    -- exactly one of (user_id, service_account_id) must be set
    user_id          text        REFERENCES users(id) ON DELETE CASCADE,
    service_account_id text      REFERENCES service_accounts(id) ON DELETE CASCADE,
    -- panel: tokens are 'midn_<class>_<26-char base32-256bit>'; we store the
    -- public 6-char prefix for display and the sha256 of the full token.
    token_class      text        NOT NULL,                            -- 'pat'|'sa'|'agent'|'ci'
    token_prefix     text        NOT NULL,                            -- public 6 chars surfaced in UI/audit
    token_hash       bytea       NOT NULL,                            -- sha256(full token)
    scopes           text[]      NOT NULL DEFAULT ARRAY[]::text[],    -- 'projects:read', 'deployments:create', …
    expires_at       timestamptz,
    last_used_at     timestamptz,
    revoked_at       timestamptz,
    revoked_reason   text,
    rotation_event_id text,                                            -- correlates rotation pairs (two-tokens overlap)
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT api_tokens_owner_chk
        CHECK ((user_id IS NULL) <> (service_account_id IS NULL)),
    CONSTRAINT api_tokens_class_chk
        CHECK (token_class IN ('pat','sa','agent','ci'))
);
CREATE TRIGGER api_tokens_rv BEFORE UPDATE ON api_tokens
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE UNIQUE INDEX api_tokens_hash_uq ON api_tokens (token_hash);
CREATE INDEX api_tokens_owner_idx ON api_tokens (COALESCE(user_id, service_account_id));
CREATE INDEX api_tokens_active_idx ON api_tokens (revoked_at) WHERE revoked_at IS NULL;
COMMENT ON TABLE api_tokens IS
    'Bearer token registry. Token plaintext is never stored; only sha256 + '
    'public prefix. Auto-revoke for unused-N-days is implemented in CLI.';

CREATE TABLE bootstrap_tokens (
    id               text        PRIMARY KEY,                         -- 'btok_…'
    token_hash       bytea       NOT NULL UNIQUE,                     -- sha256 of the plaintext written to /var/lib/midnight/bootstrap-token
    issued_at        timestamptz NOT NULL DEFAULT now(),
    consumed_at      timestamptz,                                     -- one-shot: deleted on read
    consumed_by_user_id text     REFERENCES users(id) ON DELETE SET NULL,
    auto_disabled_at timestamptz,                                     -- when a normal admin appears
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER bootstrap_tokens_rv BEFORE UPDATE ON bootstrap_tokens
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE bootstrap_tokens IS
    'Records the first-start admin bootstrap (panel: bootstrap admin token is '
    'generated on first start of midnight-server and written to '
    '0600 root:root /var/lib/midnight/bootstrap-token). One row exists for '
    'the install lifetime; consumed_at + auto_disabled_at make the bootstrap '
    'path auditable and tamper-evident.';

-- ---------------------------------------------------------------------
-- 2. Projects, environments, services
-- ---------------------------------------------------------------------

CREATE TABLE projects (
    id               text        PRIMARY KEY,                        -- 'prj_…'
    workspace_id     text        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug             text        NOT NULL,
    name             text        NOT NULL,
    description      text,
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    deleted_at       timestamptz,                                     -- soft delete; cascades to children only on hard delete
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    UNIQUE (workspace_id, slug)
);
CREATE TRIGGER projects_rv BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE projects IS 'Application stack boundary. Soft-deleted via deleted_at.';

CREATE TABLE environments (
    id               text        PRIMARY KEY,                        -- 'env_…'
    project_id       text        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slug             text        NOT NULL,                            -- 'production' / 'staging' / …
    name             text        NOT NULL,
    tier             text        NOT NULL,                            -- 'production' | 'staging' | 'preview' | 'development'
    is_protected     boolean     NOT NULL DEFAULT false,              -- production protection flag
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    UNIQUE (project_id, slug),
    CONSTRAINT environments_tier_chk
        CHECK (tier IN ('production','staging','preview','development'))
);
CREATE TRIGGER environments_rv BEFORE UPDATE ON environments
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE environments IS
    'Environment within a project. tier feeds policy decisions (e.g. cross-tier '
    'reference variable denial). production environments require approvals.';

CREATE TABLE services (
    id               text        PRIMARY KEY,                        -- 'svc_…'
    project_id       text        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- A service belongs to a project; environment-scoped state lives in
    -- service_environment_state and deployments. The service row itself is
    -- the desired-spec carrier across environments.
    slug             text        NOT NULL,                            -- RFC-1035 valid (panel: networking)
    name             text        NOT NULL,
    kind             text        NOT NULL DEFAULT 'app',              -- 'app'|'database'|'job'
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- desired spec lives as JSON, with explicit columns for fields that
    -- are constrained / indexed / referenced by FKs in other tables.
    spec             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    UNIQUE (project_id, slug),
    CONSTRAINT services_slug_rfc1035_chk
        CHECK (slug ~ '^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$')
);
CREATE TRIGGER services_rv BEFORE UPDATE ON services
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE services IS
    'Service desired-spec carrier. RFC-1035 slug is enforced by check '
    'constraint (panel: internal DNS rule).';

CREATE TABLE service_sources (
    id                 text        PRIMARY KEY,                       -- 'src_…'
    service_id         text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    source_provider    text        NOT NULL,                          -- 'github'|'gitlab'|'cli'|'image'|'template'
    source_repo        text,                                          -- 'org/repo' for git providers
    source_ref         text,                                          -- 'main' / branch / tag
    image_repository   text,                                          -- when source_provider='image'
    template_id        text,                                          -- when source_provider='template' (FK in v1+ when templates ship)
    builder_hint       text,                                          -- 'dockerfile'|'railpack'|'nixpacks'|'auto'
    dockerfile_path    text,
    service_root       text,
    config             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    is_default         boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    resource_version   bigint      NOT NULL DEFAULT 1,
    CONSTRAINT service_sources_provider_chk
        CHECK (source_provider IN ('github','gitlab','cli','image','template'))
);
CREATE TRIGGER service_sources_rv BEFORE UPDATE ON service_sources
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE UNIQUE INDEX service_sources_default_uq
    ON service_sources (service_id) WHERE is_default;
COMMENT ON TABLE service_sources IS
    'Build/deploy source-of-truth for a service. Multiple sources may exist '
    '(e.g. main branch + a release-channel image), but only one is_default=true.';

-- ---------------------------------------------------------------------
-- 3. Single-flight lock per (service, environment)
-- ---------------------------------------------------------------------

CREATE TABLE service_environment_state (
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id   text        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    -- panel: every deployment-mutation path acquires either a pg_advisory_xact_lock
    -- on hashtext('svc:'||service_id||':'||env_id) OR updates this row with
    -- optimistic concurrency. We model both primitives so transition functions
    -- can choose; production code path uses the advisory lock and updates
    -- this row inside the same transaction.
    active_deployment_id text,                                        -- FK added below (forward ref to deployments)
    last_deployment_seq bigint   NOT NULL DEFAULT 0,                  -- monotonic; never wall-clock
    last_state_change_at timestamptz NOT NULL DEFAULT now(),
    in_flight_op_id  text,                                            -- soft link to operations.id while a mutation is in progress
    locked_until     timestamptz,                                     -- soft expiry for stuck locks (advisory, not authoritative)
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (service_id, environment_id)
);
-- The deployments table is created further down; the FK is added with ALTER
-- once both tables exist (see late-bound section after deployments).
COMMENT ON TABLE service_environment_state IS
    'Single-flight lock + monotonic deployment sequence per (service, environment). '
    'Panel: runLatest arbitration uses last_deployment_seq, never wall-clock time. '
    'Pair the advisory_xact_lock with an UPDATE … RETURNING last_deployment_seq+1 '
    'inside the same transaction.';

-- ---------------------------------------------------------------------
-- 4. Variables, references, secrets boundary
-- ---------------------------------------------------------------------

CREATE TABLE variables (
    id               text        PRIMARY KEY,                        -- 'var_…'
    workspace_id     text        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- Scope columns are nullable; exactly one of (project_id, environment_id, service_id)
    -- combinations defines the scope. workspace_id is always populated.
    project_id       text        REFERENCES projects(id) ON DELETE CASCADE,
    environment_id   text        REFERENCES environments(id) ON DELETE CASCADE,
    service_id       text        REFERENCES services(id) ON DELETE CASCADE,
    name             text        NOT NULL,
    -- variable kind drives visibility, sealing, and resolution rules
    kind             text        NOT NULL,                            -- 'plain'|'secret'|'sealed'|'shared'|'reference'|'system'
    classification   text        NOT NULL,                            -- 'buildVar'|'buildSecret'|'runtimeVar'|'runtimeSecret'|'publicClientVar'|'systemVar'
    value_plain      text,                                            -- only for plain/publicClientVar
    -- secret/sealed values live in a content-addressed secret store; this
    -- column holds the reference id. The secret store itself is outside
    -- this DDL (see SCHEMA_NOTES.md for the boundary).
    secret_ref       text,                                            -- e.g. 'csa://<sha256>' content-addressed reference
    secret_version   integer,
    last_rotated_at  timestamptz,
    length_bucket    text,                                            -- 'short'|'medium'|'long' (UI-only hint for sealed)
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT variables_kind_chk
        CHECK (kind IN ('plain','secret','sealed','shared','reference','system')),
    CONSTRAINT variables_classification_chk
        CHECK (classification IN ('buildVar','buildSecret','runtimeVar','runtimeSecret','publicClientVar','systemVar')),
    CONSTRAINT variables_value_xor_chk
        -- plain values may NOT live in secret_ref; secret values may NOT live in value_plain
        CHECK (
            (kind IN ('plain','reference','system') AND secret_ref IS NULL)
         OR (kind IN ('secret','sealed') AND value_plain IS NULL)
         OR (kind = 'shared')
        )
);
CREATE TRIGGER variables_rv BEFORE UPDATE ON variables
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();

-- Uniqueness across the four possible scope shapes. Postgres partial indexes
-- give us a clean way to enforce "name is unique within its scope".
CREATE UNIQUE INDEX variables_workspace_scope_uq
    ON variables (workspace_id, name)
    WHERE project_id IS NULL AND environment_id IS NULL AND service_id IS NULL;
CREATE UNIQUE INDEX variables_project_scope_uq
    ON variables (project_id, name)
    WHERE project_id IS NOT NULL AND environment_id IS NULL AND service_id IS NULL;
CREATE UNIQUE INDEX variables_environment_scope_uq
    ON variables (environment_id, name)
    WHERE environment_id IS NOT NULL AND service_id IS NULL;
CREATE UNIQUE INDEX variables_service_scope_uq
    ON variables (service_id, environment_id, name)
    WHERE service_id IS NOT NULL;

COMMENT ON TABLE variables IS
    'Plain/secret/sealed/shared/reference/system variables. Sealed plaintext '
    'never returns from any API path; rotation requires a new value (panel). '
    'Secret values are stored in a content-addressed secret store via secret_ref.';

CREATE TABLE variable_references (
    id               text        PRIMARY KEY,                        -- 'vrf_…'
    -- The reference graph edge: a "consumer" variable points at a "target".
    consumer_variable_id text    NOT NULL REFERENCES variables(id) ON DELETE CASCADE,
    -- Reference target may be another variable, a service output (port, host),
    -- or a system-provided value resolved at deploy time.
    target_kind      text        NOT NULL,                            -- 'variable'|'service_output'|'system'
    target_variable_id text      REFERENCES variables(id) ON DELETE RESTRICT,
    target_service_id text       REFERENCES services(id) ON DELETE RESTRICT,
    target_output_key text,                                            -- 'private_url' | 'public_url' | 'port' | …
    is_secret        boolean     NOT NULL DEFAULT false,              -- propagated secrecy bit
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT variable_references_target_chk
        CHECK (
            (target_kind = 'variable'       AND target_variable_id IS NOT NULL AND target_service_id IS NULL)
         OR (target_kind = 'service_output' AND target_service_id  IS NOT NULL AND target_variable_id IS NULL)
         OR (target_kind = 'system'         AND target_variable_id IS NULL     AND target_service_id  IS NULL)
        )
);
CREATE TRIGGER variable_references_rv BEFORE UPDATE ON variable_references
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX variable_references_consumer_idx ON variable_references (consumer_variable_id);
CREATE INDEX variable_references_target_var_idx ON variable_references (target_variable_id);
CREATE INDEX variable_references_target_svc_idx ON variable_references (target_service_id);
COMMENT ON TABLE variable_references IS
    'Variable graph edges. ON DELETE RESTRICT on targets is deliberate: '
    'rename/delete of a referenced variable or service must produce explicit '
    'affected-service warnings (panel) before being allowed.';

-- ---------------------------------------------------------------------
-- 5. Domains, deployments, snapshots, image pins, instances, builds
-- ---------------------------------------------------------------------

CREATE TABLE domains (
    id               text        PRIMARY KEY,                        -- 'dom_…'
    workspace_id     text        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id       text        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id   text        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    hostname         text        NOT NULL,
    hostname_type    text        NOT NULL,                            -- 'generated'|'custom'|'wildcard_custom'
    target_port      integer     NOT NULL,
    protocol         text        NOT NULL DEFAULT 'http',             -- 'http'|'h2c'|'tcp'
    -- DNS verification
    verification_txt_name  text,                                      -- '_midnight-verify.<apex>'
    verification_txt_value text,
    ownership_verified_at  timestamptz,
    routing_verified_at    timestamptz,
    -- TLS
    tls_status       text        NOT NULL DEFAULT 'pending',          -- 'pending'|'issued'|'failed'|'renewing'
    certificate_id   text,                                            -- soft link to certificates table (v1)
    -- ACME accounting
    acme_attempts    integer     NOT NULL DEFAULT 0,
    last_acme_error  text,
    -- routing aggregate status (drives doctor and the API)
    status           text        NOT NULL DEFAULT 'pending',
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    UNIQUE (hostname),
    CONSTRAINT domains_hostname_type_chk
        CHECK (hostname_type IN ('generated','custom','wildcard_custom'))
);
CREATE TRIGGER domains_rv BEFORE UPDATE ON domains
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX domains_service_idx ON domains (service_id);
COMMENT ON TABLE domains IS
    'Domain bindings. hostname is globally unique. tls_status, ownership, and '
    'routing verification are independent and surfaced separately by doctor.';

-- The deployments table is the load-bearing one. It is referenced by many
-- other tables. Create it now.

CREATE TABLE deployments (
    id               text        PRIMARY KEY,                        -- 'dep_…'
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    environment_id   text        NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    -- monotonic per (service, environment); panel: bigserial. We model it as
    -- a regular bigint populated by transition functions to avoid a global
    -- sequence skew across services. The transition function must:
    --   UPDATE service_environment_state
    --   SET last_deployment_seq = last_deployment_seq + 1
    --   RETURNING last_deployment_seq;
    deployment_seq   bigint      NOT NULL,
    state            text        NOT NULL DEFAULT 'QUEUED',           -- public state machine
    reason_code      text,                                            -- when FAILED (failure-classification ref)
    cancel_reason    text,                                            -- when CANCELING/CANCELED
    rollback_source  text,                                            -- 'manual'|'upgrade-auto'|'cli'
    parent_deployment_id text    REFERENCES deployments(id) ON DELETE SET NULL,
    source_snapshot_id   text,                                        -- FK added below (deployment_snapshots created later)
    image_digest     text,                                            -- denormalized for hot-path index
    min_ready        integer     NOT NULL DEFAULT 1,
    actor            text,                                            -- usr_… / sa_… / 'system'
    operation_id     text,                                            -- soft link to operations.id
    idempotency_key  text,                                            -- soft link to idempotency_keys.key_id
    entered_state_at timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT deployments_state_chk
        CHECK (state IN ('QUEUED','BUILDING','DEPLOYING','ACTIVE','SUPERSEDED','FAILED','CANCELING','CANCELED','REMOVING','REMOVED')),
    CONSTRAINT deployments_seq_uq UNIQUE (service_id, environment_id, deployment_seq)
);
CREATE TRIGGER deployments_rv BEFORE UPDATE ON deployments
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX deployments_service_env_state_idx
    ON deployments (service_id, environment_id, state);
CREATE INDEX deployments_state_active_idx
    ON deployments (service_id, environment_id) WHERE state = 'ACTIVE';
CREATE INDEX deployments_image_digest_idx ON deployments (image_digest)
    WHERE image_digest IS NOT NULL;

COMMENT ON TABLE deployments IS
    'A deploy attempt with public state. ON DELETE RESTRICT on service_id and '
    'environment_id is intentional: deployments must be archived/removed via '
    'the FSM (REMOVING -> REMOVED), not cascaded. Snapshots are immutable; '
    'deployments are not.';

-- Late-bound FK for the forward reference above.
ALTER TABLE service_environment_state
    ADD CONSTRAINT service_environment_state_active_deployment_fk
    FOREIGN KEY (active_deployment_id) REFERENCES deployments(id)
    ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE deployment_snapshots (
    id               text        PRIMARY KEY,                        -- 'snp_…'
    deployment_id    text        NOT NULL UNIQUE REFERENCES deployments(id) ON DELETE RESTRICT,
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    environment_id   text        NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,

    source_provider  text,                                            -- 'github'|'cli'|'image'|'template'
    source_repo      text,
    source_sha       text,                                            -- panel: stored on snapshot

    builder_type     text        NOT NULL,                            -- 'dockerfile'|'railpack'|'nixpacks'|'image'
    builder_version  text        NOT NULL,                            -- e.g. 'railpack@v0.X.Y'
    builder_digest   text        NOT NULL,                            -- sha256 of pinned builder container
    buildkit_version text,
    dockerfile_frontend_digest text,

    -- panel-mandated immutable replay artifacts
    resolved_build_plan        jsonb NOT NULL,
    resolved_base_image_digests jsonb NOT NULL,                       -- { "FROM ref": "sha256:..." }

    image_repository text        NOT NULL,
    image_digest     text        NOT NULL,
    image_signature  text,                                            -- cosign blob or ref
    sbom_ref         text,
    provenance_ref   text,
    build_log_ref    text,
    build_cache_scope text,                                           -- '<tenant>/<project>/<service>/<builder>/<root>'

    runtime_command  text        NOT NULL,
    runtime_user     text,
    runtime_resource_limits jsonb NOT NULL,
    runtime_capabilities    jsonb,
    runtime_volumes         jsonb,
    healthcheck_spec        jsonb NOT NULL,

    -- Resolved variables: non-secrets inlined; secrets only as references.
    -- panel: "resolved_variables_blob_id (FK to content-addressed secret store)"
    resolved_runtime_vars       jsonb NOT NULL DEFAULT '{}'::jsonb,
    resolved_runtime_secret_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    resolved_build_vars         jsonb NOT NULL DEFAULT '{}'::jsonb,
    public_client_vars          jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- The full content-addressed reference id used by registry GC + snapshot export.
    -- The secret-store row is outside this DDL; the FK target is logical, not enforced.
    resolved_variables_blob_id  text,                                  -- 'csa://<sha256>'

    template_id        text,
    template_version   text,
    arch_platforms     jsonb,

    created_at       timestamptz NOT NULL DEFAULT now(),
    created_by       text        NOT NULL,
    -- snapshots are immutable: no updated_at/resource_version; an attempt to
    -- UPDATE is rejected by the row-level trigger below.
    CONSTRAINT deployment_snapshots_builder_type_chk
        CHECK (builder_type IN ('dockerfile','railpack','nixpacks','image'))
);
CREATE OR REPLACE FUNCTION midnight_reject_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'SNAPSHOT_IMMUTABLE: deployment_snapshots are append-only';
END
$$;
CREATE TRIGGER deployment_snapshots_no_update
    BEFORE UPDATE OR DELETE ON deployment_snapshots
    FOR EACH ROW EXECUTE FUNCTION midnight_reject_snapshot_mutation();
CREATE INDEX deployment_snapshots_image_digest_idx ON deployment_snapshots (image_digest);
CREATE INDEX deployment_snapshots_service_env_idx ON deployment_snapshots (service_id, environment_id, created_at DESC);
COMMENT ON TABLE deployment_snapshots IS
    'Immutable, fully-self-contained replay record for a deployment. Rollback '
    'replays from this row without re-invoking Railpack/BuildKit (panel: '
    'rollback never rebuilds). Mutations are rejected at the row level.';

-- Now wire deployments.source_snapshot_id forward.
ALTER TABLE deployments
    ADD CONSTRAINT deployments_source_snapshot_fk
    FOREIGN KEY (source_snapshot_id) REFERENCES deployment_snapshots(id) ON DELETE RESTRICT;

CREATE TABLE deployment_image_pins (
    digest           text        NOT NULL,                            -- sha256:...
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    deployment_id    text        NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    reason           text        NOT NULL,                            -- 'active'|'rollback_window'|'promotion'|'restore_manifest'|'audit_retention'
    pinned_until     timestamptz,                                     -- null = while reason holds
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (digest, service_id, deployment_id, reason),
    CONSTRAINT deployment_image_pins_reason_chk
        CHECK (reason IN ('active','rollback_window','promotion','restore_manifest','audit_retention'))
);
CREATE TRIGGER deployment_image_pins_rv BEFORE UPDATE ON deployment_image_pins
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
-- Hot path: GC probe by digest.
CREATE INDEX deployment_image_pins_digest_active_idx
    ON deployment_image_pins (digest)
    WHERE pinned_until IS NULL OR pinned_until > now();
COMMENT ON TABLE deployment_image_pins IS
    'Reference count for digests still required by an active deployment, an '
    'in-window rollback target, an artifact promotion, a restore manifest, or '
    'audit retention. Registry GC consults this table on every sweep '
    '(panel: digests with pin_count > 0 are unreclaimable).';
-- OPEN QUESTION: the predicate "pinned_until IS NULL OR pinned_until > now()"
-- in the index above is non-immutable (uses now()), so Postgres marks the
-- index as "predicate uses non-IMMUTABLE function". This is acceptable: the
-- index is correct at write time; a daily job sweeps stale rows. Confirm
-- the registry GC worker performs the date check at query time, not relying
-- on the partial-index predicate to be re-evaluated.

CREATE TABLE builds (
    id               text        PRIMARY KEY,                        -- 'bld_…'
    deployment_id    text        REFERENCES deployments(id) ON DELETE CASCADE,
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    state            text        NOT NULL DEFAULT 'queued',           -- 'queued'|'running'|'succeeded'|'failed'|'canceled'
    builder_type     text        NOT NULL,
    builder_version  text        NOT NULL,
    builder_digest   text        NOT NULL,
    buildkit_version text,
    image_repository text,
    image_digest     text,                                            -- sha256:... when succeeded
    image_signature  text,
    cache_scope      text,
    started_at       timestamptz,
    finished_at      timestamptz,
    duration_ms      bigint      GENERATED ALWAYS AS
                       (EXTRACT(EPOCH FROM (finished_at - started_at))*1000)::bigint STORED,
    log_ref          text,
    sbom_ref         text,
    provenance_ref   text,
    is_untrusted     boolean     NOT NULL DEFAULT false,              -- panel: untrusted-build classification
    reason_code      text,                                            -- on failure
    actor            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT builds_state_chk
        CHECK (state IN ('queued','running','succeeded','failed','canceled'))
);
CREATE TRIGGER builds_rv BEFORE UPDATE ON builds
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX builds_state_idx ON builds (state, created_at DESC);
CREATE INDEX builds_service_idx ON builds (service_id, created_at DESC);
CREATE INDEX builds_image_digest_idx ON builds (image_digest) WHERE image_digest IS NOT NULL;
COMMENT ON TABLE builds IS
    'Build worker execution record. Multiple builds per deployment are allowed '
    '(retries); duration_ms is a stored generated column for SLO dashboards.';

-- ---------------------------------------------------------------------
-- 6. Hosts, heartbeats (partitioned), routes
-- ---------------------------------------------------------------------

CREATE TABLE hosts (
    id               text        PRIMARY KEY,                        -- 'hst_…'
    workspace_id     text        REFERENCES workspaces(id) ON DELETE SET NULL,
    -- panel: agents register against the control plane; in Alpha the host is
    -- an instance-scoped resource shared across workspaces (single-node
    -- profile). workspace_id is informational, not a tenancy boundary.
    name             text        NOT NULL UNIQUE,
    hostname         text        NOT NULL,
    arch             text        NOT NULL,                            -- 'amd64'|'arm64'
    os               text        NOT NULL,                            -- 'ubuntu-24.04'|'debian-12'|...
    agent_version    text,
    docker_version   text,
    capacity         jsonb       NOT NULL DEFAULT '{}'::jsonb,        -- cpu, memory, disk, inode budgets
    labels           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    state            text        NOT NULL DEFAULT 'unknown',          -- 'unknown'|'healthy'|'degraded'|'lost'
    last_heartbeat_at timestamptz,
    clock_offset_ms  integer,                                         -- agent reports; control plane refuses |offset| > 30s
    registered_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER hosts_rv BEFORE UPDATE ON hosts
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX hosts_state_idx ON hosts (state) WHERE deleted_at IS NULL;
COMMENT ON TABLE hosts IS
    'Registered host agents. last_heartbeat_at and state drive instance '
    'aggregation; clock_offset_ms enforces AGENT_CLOCK_SKEW (panel).';

-- Range-partitioned heartbeats. Monthly. PK includes the partition key.
CREATE TABLE host_heartbeats (
    host_id          text        NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    occurred_at      timestamptz NOT NULL,
    agent_version    text,
    cpu_pct          real,
    mem_used_bytes   bigint,
    disk_used_bytes  bigint,
    inode_used_pct   real,
    conntrack_pct    real,                                            -- panel: conntrack pressure is a required metric
    clock_offset_ms  integer,
    docker_status    text,                                            -- 'ok'|'unavailable'|'unsupported'
    payload          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (host_id, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX host_heartbeats_recent_idx
    ON host_heartbeats (host_id, occurred_at DESC);
COMMENT ON TABLE host_heartbeats IS
    'High-write host telemetry. Monthly range partitions; a daily pruning '
    'worker drops partitions past retention. Used by reconciler to compute '
    'instance.last_observed_at and to fire AgentLost/HOST_UNAVAILABLE.';

-- Bootstrap partitions (current and next month). Operator/runtime worker
-- creates additional partitions; pg_partman is the recommended automation.
CREATE TABLE host_heartbeats_2026_04 PARTITION OF host_heartbeats
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE host_heartbeats_2026_05 PARTITION OF host_heartbeats
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');

CREATE TABLE routes (
    id               text        PRIMARY KEY,                        -- 'rte_…'
    domain_id        text        NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    service_id       text        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id   text        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    deployment_id    text        REFERENCES deployments(id) ON DELETE SET NULL,
    target_port      integer     NOT NULL,
    protocol         text        NOT NULL DEFAULT 'http',
    target_ready_at  timestamptz,                                     -- panel: DEPLOYING -> ACTIVE guard requires NOT NULL
    last_observed_at timestamptz,                                     -- route reconciler heartbeat
    route_freshness_ttl_seconds integer NOT NULL DEFAULT 30,
    status           text        NOT NULL DEFAULT 'pending',          -- 'pending'|'ready'|'draining'|'failed'
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER routes_rv BEFORE UPDATE ON routes
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
-- Route freshness probe: deployment promotion + reconciler use this index.
CREATE INDEX routes_deployment_target_ready_idx
    ON routes (deployment_id) WHERE target_ready_at IS NOT NULL;
CREATE INDEX routes_freshness_idx
    ON routes (last_observed_at) WHERE status = 'ready';
COMMENT ON TABLE routes IS
    'Per-deployment route records. target_ready_at is part of the '
    'DEPLOYING -> ACTIVE guard (panel: deployment reconciler). '
    'last_observed_at + route_freshness_ttl_seconds enforce LOST instance '
    'removal hysteresis.';

-- ---------------------------------------------------------------------
-- 7. Deployment instances (partitioned)
-- ---------------------------------------------------------------------

CREATE TABLE deployment_instances (
    id               text        NOT NULL,                            -- 'inst_…'
    deployment_id    text        NOT NULL,                            -- soft FK; cross-partition FK is awkward
    service_id       text        NOT NULL,
    host_id          text        NOT NULL,
    container_id     text,                                            -- docker container id once created
    state            text        NOT NULL DEFAULT 'ALLOCATED',
    ready_count      integer     NOT NULL DEFAULT 0,
    last_observed_at timestamptz,
    started_at       timestamptz,
    stopped_at       timestamptz,
    crash_count      integer     NOT NULL DEFAULT 0,
    last_reason_code text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (id, created_at),
    CONSTRAINT deployment_instances_state_chk
        CHECK (state IN ('ALLOCATED','PULLING','CREATING','STARTING','RUNNING','READY','UNHEALTHY','CRASHED','RESTARTING','DRAINING','STOPPED','REMOVED','LOST'))
) PARTITION BY RANGE (created_at);
-- NOTE: cross-partition FK to deployments(id) is not enforced; the soft link is
-- correct because deployment_instances has its own retention and may outlive
-- a deployment row's lifetime in a hot-path partition. The reconciler joins
-- on deployment_id at read time.
CREATE INDEX deployment_instances_deployment_idx
    ON deployment_instances (deployment_id, state);
CREATE INDEX deployment_instances_host_idx
    ON deployment_instances (host_id, state);
-- Hot path: route reconciler joins on (deployment_id, state='READY') count.
CREATE INDEX deployment_instances_ready_idx
    ON deployment_instances (deployment_id) WHERE state = 'READY';
CREATE TABLE deployment_instances_2026_04 PARTITION OF deployment_instances
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE deployment_instances_2026_05 PARTITION OF deployment_instances
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
COMMENT ON TABLE deployment_instances IS
    'Observed instance state, owned by the agent via reports. Aggregated by '
    'the reconciler into deployment state. Monthly partitions; freshness TTL '
    'governs route membership (panel).';

-- ---------------------------------------------------------------------
-- 8. Events (partitioned), audit logs (partitioned), audit checkpoints
-- ---------------------------------------------------------------------

CREATE TABLE events (
    id               bigserial   NOT NULL,
    occurred_at      timestamptz NOT NULL DEFAULT now(),
    workspace_id     text,
    project_id       text,
    environment_id   text,
    service_id       text,
    deployment_id    text,
    request_id       text,                                            -- correlation
    ce_id            text        NOT NULL,                            -- CloudEvents id
    ce_type          text        NOT NULL,                            -- e.g. 'dev.midnight.deployment.active'
    ce_subject       text,
    ce_source        text        NOT NULL,
    actor            text,
    payload          jsonb       NOT NULL,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX events_subject_time_idx ON events (ce_subject, occurred_at DESC);
CREATE INDEX events_request_id_idx   ON events (request_id) WHERE request_id IS NOT NULL;
CREATE INDEX events_deployment_idx   ON events (deployment_id, occurred_at DESC) WHERE deployment_id IS NOT NULL;
CREATE TABLE events_2026_04 PARTITION OF events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE events_2026_05 PARTITION OF events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
COMMENT ON TABLE events IS
    'CloudEvents 1.0-shaped event log used by webhooks, SSE, and the '
    'deployment timeline. Append-only; partitioned monthly. id is bigserial '
    'because CloudEvents.id is a string and the surrogate id keeps ordering.';

CREATE TABLE audit_logs (
    id               bigserial   NOT NULL,
    -- chain integrity (panel: per-row hash chain rooted in audit_checkpoints)
    prev_hash        bytea       NOT NULL,
    row_hash         bytea       NOT NULL,
    checkpoint_id    bigint,                                          -- null until next checkpoint signs over this row
    canonical_form_version text  NOT NULL DEFAULT 'v1',

    -- correlation
    request_id       text        NOT NULL,
    trace_id         text,
    occurred_at      timestamptz NOT NULL DEFAULT now(),
    recorded_at      timestamptz NOT NULL DEFAULT now(),

    -- actor
    actor_id         text,                                            -- usr_… / sa_… / null for system
    actor_type       text        NOT NULL,                            -- 'user'|'service_account'|'system'|'mcp_client'
    auth_method      text,                                            -- 'password+totp'|'oidc'|'bearer'|'bootstrap'|'system'
    source_ip        inet,
    user_agent       text,

    -- subject
    workspace_id     text,
    project_id       text,
    environment_id   text,
    resource_kind    text        NOT NULL,
    resource_id      text,
    action           text        NOT NULL,

    -- payload (redacted; never plaintext secrets)
    before           jsonb,
    after            jsonb,
    reason_code      text,
    policy_decision  text,                                            -- 'allow'|'deny'|'allow_with_approval'
    attrs            jsonb,

    PRIMARY KEY (id, occurred_at),
    CONSTRAINT audit_logs_actor_type_chk
        CHECK (actor_type IN ('user','service_account','system','mcp_client'))
) PARTITION BY RANGE (occurred_at);
CREATE INDEX audit_logs_subject_idx
    ON audit_logs (resource_kind, resource_id, occurred_at DESC);
CREATE INDEX audit_logs_actor_idx
    ON audit_logs (actor_id, occurred_at DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_logs_request_idx
    ON audit_logs (request_id);
-- Hot path: chain verify reads in id order across the active partition; the
-- partition's primary key already covers (id, occurred_at) ordering.
CREATE INDEX audit_logs_checkpoint_idx
    ON audit_logs (checkpoint_id) WHERE checkpoint_id IS NOT NULL;
CREATE TABLE audit_logs_2026_04 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE audit_logs_2026_05 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
COMMENT ON TABLE audit_logs IS
    'Append-only audit log with per-row hash chaining: '
    'row_hash = SHA-256(prev_hash || canonical_row). The grant table at the '
    'bottom of this file revokes UPDATE/DELETE from midnight_app; only '
    'midnight_migrate has DDL. Tampering is detectable by midnight audit verify.';

CREATE TABLE audit_checkpoints (
    id                       bigserial   PRIMARY KEY,
    partition_label          text        NOT NULL,                    -- '2026-04'
    started_at               timestamptz NOT NULL,
    ended_at                 timestamptz NOT NULL,
    first_row_id             bigint      NOT NULL,
    last_row_id              bigint      NOT NULL,
    last_row_hash            bytea       NOT NULL,
    signature_alg            text        NOT NULL,                    -- 'ed25519'
    signing_key_fingerprint  text        NOT NULL,                    -- sha256 of public key, hex
    signature                bytea       NOT NULL,
    prev_checkpoint_id       bigint      REFERENCES audit_checkpoints(id) ON DELETE RESTRICT,
    is_final                 boolean     NOT NULL DEFAULT false,      -- closes a partition before pruning
    created_at               timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_checkpoints_partition_idx
    ON audit_checkpoints (partition_label, ended_at DESC);
COMMENT ON TABLE audit_checkpoints IS
    'Daily signed checkpoints that root the audit chain. Operator-configured '
    'Ed25519 key recommended in production; Midnight release key is the '
    'fallback. is_final closes a partition so prune may proceed (panel).';

-- ---------------------------------------------------------------------
-- 9. Jobs (partitioned), idempotency keys, operations, failure classes
-- ---------------------------------------------------------------------

CREATE TABLE jobs (
    id               text        NOT NULL,                            -- 'job_…'
    queue            text        NOT NULL,                            -- 'build'|'deploy'|'reconcile'|'webhook'|'cleanup'
    partition_key    text        NOT NULL,                            -- per-tenant fairness key, e.g. workspace_id
    state            text        NOT NULL DEFAULT 'pending',          -- 'pending'|'leased'|'succeeded'|'failed'|'dead_letter'|'canceled'
    priority         integer     NOT NULL DEFAULT 0,                  -- higher first
    available_at     timestamptz NOT NULL DEFAULT now(),              -- visible to dequeue when <= now()
    lease_owner      text,                                            -- worker_id; nullable
    lease_expires_at timestamptz,                                     -- null when not leased
    heartbeat_at     timestamptz,
    attempt          integer     NOT NULL DEFAULT 0,
    max_attempts     integer     NOT NULL DEFAULT 5,
    payload          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    parent_operation_id text,                                          -- soft link to operations.id (panel: keys outlive operations)
    reason_code      text,
    dead_letter_reason text,
    finished_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (id, created_at),
    CONSTRAINT jobs_queue_chk
        CHECK (queue IN ('build','deploy','reconcile','webhook','cleanup')),
    CONSTRAINT jobs_state_chk
        CHECK (state IN ('pending','leased','succeeded','failed','dead_letter','canceled'))
) PARTITION BY RANGE (created_at);

-- Hot-path dequeue index. The exact predicate from the panel decision is:
--   "(queue, available_at, priority) WHERE lease_expires_at IS NULL OR lease_expires_at < now()".
-- Because now() is non-IMMUTABLE, Postgres rejects it inside an index
-- predicate; we approximate with state='pending', which is monotonically
-- equivalent (a row can only be 'pending' if its lease has expired or never
-- existed). The dequeue SQL still applies the runtime predicate.
CREATE INDEX jobs_dequeue_idx
    ON jobs (queue, priority DESC, available_at ASC)
    WHERE state = 'pending';
CREATE INDEX jobs_reaper_idx
    ON jobs (state, lease_expires_at)
    WHERE state = 'leased';
CREATE INDEX jobs_partition_key_idx
    ON jobs (partition_key, queue, state);
CREATE INDEX jobs_parent_op_idx ON jobs (parent_operation_id) WHERE parent_operation_id IS NOT NULL;
CREATE TABLE jobs_2026_04 PARTITION OF jobs
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE jobs_2026_05 PARTITION OF jobs
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
COMMENT ON TABLE jobs IS
    'Postgres-backed durable queue. Dequeue uses FOR UPDATE SKIP LOCKED '
    'against jobs_dequeue_idx (panel: dequeue contract). Lease/heartbeat/'
    'reclaim semantics are normative; see SCHEMA_QUEUE_CONTRACT.md.';

CREATE TABLE idempotency_keys (
    -- Scope = (server actor, canonical tenant, method, route template, key).
    -- Generation fences retain expired history without allowing ABA writes.
    key_id           text        NOT NULL,                            -- 'idem_…' (the client-supplied key normalized)
    actor            text        NOT NULL,                            -- stable server-derived principal
    tenant           text        NOT NULL,                            -- canonical workspace/install scope
    method           text        NOT NULL,                            -- 'POST'|'PATCH'|'DELETE'
    path_template    text        NOT NULL,                            -- '/api/v1alpha/services/{svc_id}/deployments'
    body_hash        bytea       NOT NULL,                            -- sha256 over RFC 8785 canonical JSON
    generation       text        NOT NULL,                            -- immutable random attempt fence
    response_operation_id text,                                       -- FK added below (forward ref to operations)
    -- response cache for endpoints that return synchronously (no operation)
    response_status  integer,
    response_body    jsonb,
    response_headers jsonb,
    status           text        NOT NULL DEFAULT 'reserved',         -- includes retained 'expired' history
    expires_at       timestamptz NOT NULL,                            -- panel: TTL 24h; >= operation.expires_at
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    PRIMARY KEY (actor, tenant, method, path_template, key_id, generation),
    CONSTRAINT idempotency_keys_status_chk
        CHECK (status IN ('reserved','in_progress','succeeded','failed','expired'))
);
CREATE TRIGGER idempotency_keys_rv BEFORE UPDATE ON idempotency_keys
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX idempotency_keys_expiry_idx ON idempotency_keys (expires_at);
CREATE UNIQUE INDEX idempotency_keys_active_scope_uq
    ON idempotency_keys (actor, tenant, method, path_template, key_id)
    WHERE status IN ('reserved','in_progress','succeeded','failed');
COMMENT ON TABLE idempotency_keys IS
    'Idempotency contract: scope=(actor,tenant,method,path_template,key); '
    'body=sha256 over RFC 8785 canonical JSON; TTL 24h; reusing a key with a '
    'different body returns 409; generation fences retained expired attempts. '
    'expires_at must be >= operations.expires_at; '
    'the cleanup worker enforces "operation GC forbidden before key GC".';

CREATE TABLE operations (
    id               text        PRIMARY KEY,                         -- 'op_…'
    kind             text        NOT NULL,                            -- 'deploy'|'rollback'|'restore'|'backup'|'apply_change_set'|...
    target_resource_kind text    NOT NULL,                            -- 'deployment'|'service'|'environment'|...
    target_resource_id   text    NOT NULL,
    status           text        NOT NULL DEFAULT 'pending',          -- 'pending'|'running'|'succeeded'|'failed'|'canceled'
    result           jsonb,
    error            jsonb,
    actor            text        NOT NULL,
    -- soft link back to idempotency key; FK is in the other direction so
    -- multi-step idempotency replay returns the same op_id.
    idempotency_key  text,
    expires_at       timestamptz NOT NULL,
    started_at       timestamptz,
    finished_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1,
    CONSTRAINT operations_status_chk
        CHECK (status IN ('pending','running','succeeded','failed','canceled'))
);
CREATE TRIGGER operations_rv BEFORE UPDATE ON operations
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
CREATE INDEX operations_target_idx
    ON operations (target_resource_kind, target_resource_id, created_at DESC);
CREATE INDEX operations_status_idx
    ON operations (status) WHERE status IN ('pending','running');
CREATE INDEX operations_actor_idx ON operations (actor, created_at DESC);
CREATE INDEX operations_expires_idx ON operations (expires_at);
COMMENT ON TABLE operations IS
    'Public async-promise resource. result/error are JSON for forward '
    'compatibility; expires_at >= idempotency_keys.expires_at when an idem '
    'key is set (panel).';

-- Late-bound FK from idempotency_keys -> operations.
ALTER TABLE idempotency_keys
    ADD CONSTRAINT idempotency_keys_operations_fk
    FOREIGN KEY (response_operation_id) REFERENCES operations(id)
    ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE failure_classifications (
    code             text        PRIMARY KEY,                         -- e.g. 'BUILD_FAILED'
    http_status      integer,
    retriable        boolean     NOT NULL,
    terminal         boolean     NOT NULL,
    description      text        NOT NULL,
    fix_hint         text,
    introduced_in    text,                                            -- semver string
    deprecated_in    text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    resource_version bigint      NOT NULL DEFAULT 1
);
CREATE TRIGGER failure_classifications_rv BEFORE UPDATE ON failure_classifications
    FOR EACH ROW EXECUTE FUNCTION midnight_bump_resource_version();
COMMENT ON TABLE failure_classifications IS
    'Registry mirror of docs/error-codes.md. Codes are additive-only; renames '
    'require a v-bump (panel). The seed data lives in a migration.';

-- ---------------------------------------------------------------------
-- 10. Grants and role separation (panel: security/trust boundary)
-- ---------------------------------------------------------------------

-- midnight_app: read/write/insert on operational tables, NO update/delete on
-- audit tables. statement_timeout/idle_in_transaction_session_timeout are set
-- via ALTER ROLE in a separate migration (see SCHEMA_NOTES.md).
GRANT SELECT, INSERT, UPDATE, DELETE ON
    users, workspaces, workspace_members, service_accounts, api_tokens,
    bootstrap_tokens, projects, environments, services, service_sources,
    service_environment_state, variables, variable_references, domains,
    deployments, deployment_snapshots, deployment_image_pins, builds,
    hosts, routes, jobs, idempotency_keys, operations,
    failure_classifications
TO midnight_app;

-- Append-only tables: app may INSERT and SELECT but never UPDATE/DELETE.
GRANT SELECT, INSERT ON events, audit_logs TO midnight_app;
-- Heartbeats and instances: hot-write inserts; UPDATEs are allowed (state
-- transitions on instances; never on audit/events).
GRANT SELECT, INSERT, UPDATE ON host_heartbeats, deployment_instances TO midnight_app;
-- Audit checkpoints: only the audit-checkpoint worker (running as
-- midnight_app) writes; no UPDATE/DELETE.
GRANT SELECT, INSERT ON audit_checkpoints TO midnight_app;

-- Sequences used by bigserial PKs.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO midnight_app;

-- midnight_audit_ro: read-only on audit tables and event tables.
GRANT SELECT ON audit_logs, audit_checkpoints, events TO midnight_audit_ro;

-- midnight_migrate: full DDL + seed authority. CREATE/ALTER/DROP via role.
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO midnight_migrate;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO midnight_migrate;

-- Default privileges for future tables created by midnight_migrate.
ALTER DEFAULT PRIVILEGES FOR ROLE midnight_migrate IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO midnight_app;
ALTER DEFAULT PRIVILEGES FOR ROLE midnight_migrate IN SCHEMA public
    GRANT SELECT ON TABLES TO midnight_audit_ro;

-- ---------------------------------------------------------------------
-- 11. Open questions surfaced by spec gaps
-- ---------------------------------------------------------------------
-- OPEN QUESTION: Spec line 169 reserves the prefix `usr` for users; the role
--   model also references workspace owners but does not pin a prefix for
--   workspaces. We use `ws_` here; confirm in the ULID prefix taxonomy.
-- OPEN QUESTION: Spec line 484 lists `service_sources` but does not pin its
--   field set; the columns above are derived from the build-plane section
--   plus the API surface in FEATURE_DEPLOYMENT_STATE_MACHINE.md. Confirm.
-- OPEN QUESTION: Spec mandates "resolved_variables_blob_id (FK to content-
--   addressed secret store)" but the secret-store table is outside Phase 0
--   Alpha (it is referenced by id only). Confirm the secret store itself
--   gets a Phase 0 Alpha table, or remains an external store with the FK
--   modeled as a logical reference (current choice).
-- OPEN QUESTION: deployment_instances cross-partition FK to deployments(id)
--   is unenforced; spec is silent on whether instance retention should
--   match deployment retention. We treat instances as independently
--   retained partitioned data and join on read.
-- OPEN QUESTION: events.id is bigserial here, but spec line 169 reserves
--   prefix `evt` suggesting a ULID. Two options: (a) keep bigserial and add
--   a separate ce_id text column (current choice; matches the "events.id is
--   internal" view of events), or (b) make events.id a ULID `evt_…` and
--   surface that as the public CloudEvents id. Confirm before v1 graduation.
-- OPEN QUESTION: routes.deployment_id is ON DELETE SET NULL because routes
--   may briefly outlive the deployment they pointed at during drain. Spec
--   does not pin; revisit during reconciler hardening.

-- =====================================================================
-- End of SCHEMA_PHASE_0_ALPHA.sql
-- =====================================================================
