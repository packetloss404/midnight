-- 0063 — durable authority ledger for provider-neutral DNS reconciliation.
--
-- Provider credentials and plaintext DNS content are intentionally absent.
-- The content digest is the drift fence: an externally repurposed provider
-- record cannot be updated or deleted merely because its opaque id still
-- matches an older Midnight observation.
CREATE TABLE dns_record_owners (
    provider TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (provider, zone_id, owner_kind, owner_id),
    CONSTRAINT dns_record_owners_provider_chk CHECK (
        provider ~ '^[a-z][a-z0-9_-]{0,63}$'
    ),
    CONSTRAINT dns_record_owners_zone_chk CHECK (
        length(btrim(zone_id)) BETWEEN 1 AND 255
    ),
    CONSTRAINT dns_record_owners_owner_kind_chk CHECK (
        owner_kind ~ '^[a-z][a-z0-9_-]{0,63}$'
    ),
    CONSTRAINT dns_record_owners_owner_id_chk CHECK (
        length(btrim(owner_id)) BETWEEN 1 AND 255
    ),
    CONSTRAINT dns_record_owners_project_workspace_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT dns_record_owners_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE RESTRICT
);

CREATE INDEX idx_dns_record_owners_scope
    ON dns_record_owners (
        workspace_id, project_id, environment_id, owner_kind, owner_id
    );

CREATE TABLE dns_record_ownerships (
    provider TEXT NOT NULL,
    zone_id TEXT NOT NULL,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    provider_record_id TEXT NOT NULL,
    record_type TEXT NOT NULL,
    record_name TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    ttl INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        provider, zone_id, owner_kind, owner_id, record_type, record_name
    ),
    CONSTRAINT dns_record_ownerships_provider_record_uniq
        UNIQUE (provider, zone_id, provider_record_id),
    CONSTRAINT dns_record_ownerships_provider_record_id_chk CHECK (
        length(btrim(provider_record_id)) BETWEEN 1 AND 255
    ),
    CONSTRAINT dns_record_ownerships_type_chk CHECK (
        record_type IN ('A', 'AAAA', 'CNAME', 'TXT')
    ),
    CONSTRAINT dns_record_ownerships_name_chk CHECK (
        record_name = lower(record_name)
        AND length(record_name) BETWEEN 3 AND 253
        AND record_name !~ '[*:/\\`[:space:]]'
        AND record_name ~ '^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)+$'
    ),
    CONSTRAINT dns_record_ownerships_content_digest_chk CHECK (
        content_sha256 ~ '^[a-f0-9]{64}$'
    ),
    CONSTRAINT dns_record_ownerships_ttl_chk CHECK (
        ttl = 1 OR ttl BETWEEN 60 AND 86400
    ),
    CONSTRAINT dns_record_ownerships_owner_fk
        FOREIGN KEY (provider, zone_id, owner_kind, owner_id)
        REFERENCES dns_record_owners (provider, zone_id, owner_kind, owner_id)
        ON DELETE CASCADE
);

CREATE INDEX idx_dns_record_ownerships_owner
    ON dns_record_ownerships (
        provider, zone_id, owner_kind, owner_id, record_type, record_name
    );

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (63, 'dns_record_ownerships', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
