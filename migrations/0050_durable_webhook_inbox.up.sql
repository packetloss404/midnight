-- 0050 — durable verified-webhook inbox, immutable target snapshots, leased
-- retries, and dead-letter evidence.
--
-- Migration 0005 recorded a delivery as accepted/processed before synchronous
-- dispatch and retained no payload. Preserve those rows as inert history:
-- they cannot be retried safely. The replacement table acknowledges a
-- verified delivery only after its complete bounded payload is durable.

ALTER TABLE webhook_deliveries
    RENAME TO webhook_deliveries_v5_history;

CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    endpoint_id TEXT NOT NULL,
    provider_delivery_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_id TEXT NOT NULL DEFAULT '',
    event_hash TEXT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    payload_bytes BIGINT NOT NULL,
    payload BYTEA,
    signature_key_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending',
    route_attempt INTEGER NOT NULL DEFAULT 0,
    max_route_attempts INTEGER NOT NULL DEFAULT 8,
    manual_retries INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    routing_completed_at TIMESTAMPTZ,
    reason_code TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    replay_count BIGINT NOT NULL DEFAULT 0,
    identity_mismatch_count BIGINT NOT NULL DEFAULT 0,
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    terminal_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT webhook_deliveries_identity_uniq
        UNIQUE (provider, endpoint_id, provider_delivery_id),
    CONSTRAINT webhook_deliveries_id_received_uniq
        UNIQUE (id, received_at),
    CONSTRAINT webhook_deliveries_provider_chk
        CHECK (
            provider IN (
                'github',
                'gitlab-generic',
                'bitbucket',
                'midnight-incoming'
            )
        ),
    CONSTRAINT webhook_deliveries_endpoint_chk
        CHECK (
            BTRIM(endpoint_id) = endpoint_id
            AND endpoint_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$'
            AND CHAR_LENGTH(endpoint_id) <= 128
        ),
    CONSTRAINT webhook_deliveries_provider_delivery_chk
        CHECK (
            BTRIM(provider_delivery_id) = provider_delivery_id
            AND provider_delivery_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$'
            AND CHAR_LENGTH(provider_delivery_id) <= 128
        ),
    CONSTRAINT webhook_deliveries_event_type_chk
        CHECK (
            BTRIM(event_type) = event_type
            AND event_type <> ''
            AND event_type !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(event_type) <= 128
        ),
    CONSTRAINT webhook_deliveries_event_id_chk
        CHECK (
            event_id !~ '[[:cntrl:]]'
            AND CHAR_LENGTH(event_id) <= 512
        ),
    CONSTRAINT webhook_deliveries_hashes_chk
        CHECK (
            event_hash ~ '^[0-9a-f]{64}$'
            AND payload_sha256 ~ '^[0-9a-f]{64}$'
        ),
    CONSTRAINT webhook_deliveries_payload_chk
        CHECK (
            payload_bytes BETWEEN 0 AND 26214400
            AND (
                (payload IS NOT NULL AND OCTET_LENGTH(payload) = payload_bytes)
                OR (
                    payload IS NULL
                    AND state IN ('succeeded', 'ignored', 'dead_letter')
                )
            )
        ),
    CONSTRAINT webhook_deliveries_signature_key_chk
        CHECK (
            signature_key_id !~ '[[:cntrl:]]'
            AND CHAR_LENGTH(signature_key_id) <= 255
        ),
    CONSTRAINT webhook_deliveries_state_chk
        CHECK (
            state IN (
                'pending',
                'processing',
                'retry_wait',
                'succeeded',
                'ignored',
                'dead_letter'
            )
        ),
    CONSTRAINT webhook_deliveries_attempts_chk
        CHECK (
            route_attempt >= 0
            AND max_route_attempts BETWEEN 1 AND 64
            AND manual_retries >= 0
            AND replay_count >= 0
            AND identity_mismatch_count >= 0
        ),
    CONSTRAINT webhook_deliveries_lease_chk
        CHECK (
            (
                state = 'processing'
                AND lease_token IS NOT NULL
                AND BTRIM(lease_token) <> ''
                AND CHAR_LENGTH(lease_token) <= 255
                AND lease_expires_at IS NOT NULL
            )
            OR (
                state <> 'processing'
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),
    CONSTRAINT webhook_deliveries_terminal_chk
        CHECK (
            (
                state IN ('succeeded', 'ignored', 'dead_letter')
                AND terminal_at IS NOT NULL
            )
            OR (
                state NOT IN ('succeeded', 'ignored', 'dead_letter')
                AND terminal_at IS NULL
            )
        ),
    CONSTRAINT webhook_deliveries_routing_chk
        CHECK (
            routing_completed_at IS NULL
            OR routing_completed_at >= received_at
        ),
    CONSTRAINT webhook_deliveries_reason_chk
        CHECK (
            reason_code !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(reason_code) <= 128
            AND CHAR_LENGTH(last_error) <= 512
        ),
    CONSTRAINT webhook_deliveries_timestamps_chk
        CHECK (
            available_at >= received_at
            AND last_received_at >= received_at
            AND updated_at >= received_at
            AND (terminal_at IS NULL OR terminal_at >= received_at)
            AND (
                lease_expires_at IS NULL
                OR lease_expires_at > updated_at
            )
        )
);

CREATE INDEX webhook_inbox_due_idx
    ON webhook_deliveries (
        available_at,
        received_at,
        id
    )
    WHERE state IN ('pending', 'processing', 'retry_wait');

CREATE INDEX webhook_inbox_endpoint_received_idx
    ON webhook_deliveries (
        provider,
        endpoint_id,
        received_at DESC,
        id
    );

CREATE INDEX webhook_inbox_terminal_idx
    ON webhook_deliveries (
        state,
        terminal_at DESC,
        id
    )
    WHERE state IN ('succeeded', 'ignored', 'dead_letter');

-- Target rows are immutable source-authority snapshots plus mutable delivery
-- state. They intentionally do not reference live service/link/grant rows:
-- source deletion or revocation must not erase webhook evidence. The composite
-- delivery FK pins the exact database receipt time used by every temporal
-- cutoff check.
CREATE TABLE webhook_delivery_targets (
    id TEXT PRIMARY KEY,
    delivery_id TEXT NOT NULL,
    delivery_received_at TIMESTAMPTZ NOT NULL,
    target_key TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    installation_grant_resource_version BIGINT NOT NULL,
    installation_grant_created_at TIMESTAMPTZ NOT NULL,
    installation_grant_updated_at TIMESTAMPTZ NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_full_name TEXT NOT NULL,
    repository_grant_resource_version BIGINT NOT NULL,
    repository_grant_created_at TIMESTAMPTZ NOT NULL,
    repository_grant_updated_at TIMESTAMPTZ NOT NULL,
    repository_access_verified_at TIMESTAMPTZ NOT NULL,
    branch TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    ref TEXT NOT NULL,
    clone_url TEXT NOT NULL,
    root_directory TEXT NOT NULL DEFAULT '',
    config_path TEXT NOT NULL DEFAULT '',
    trigger_reason TEXT NOT NULL,
    link_resource_version BIGINT NOT NULL,
    link_created_at TIMESTAMPTZ NOT NULL,
    link_updated_at TIMESTAMPTZ NOT NULL,
    link_repository_access_verified_at TIMESTAMPTZ NOT NULL,
    dispatch_payload BYTEA,
    dispatch_payload_sha256 TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 8,
    manual_retries INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    reason_code TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    operation_id TEXT,
    deployment_id TEXT,
    staged_outbox_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    terminal_at TIMESTAMPTZ,
    CONSTRAINT webhook_delivery_targets_delivery_fk
        FOREIGN KEY (delivery_id, delivery_received_at)
        REFERENCES webhook_deliveries (id, received_at)
        ON DELETE CASCADE,
    CONSTRAINT webhook_delivery_targets_identity_uniq
        UNIQUE (delivery_id, target_key),
    CONSTRAINT webhook_delivery_targets_target_key_chk
        CHECK (
            target_key ~ '^[0-9a-f]{64}$'
        ),
    CONSTRAINT webhook_delivery_targets_scope_chk
        CHECK (
            BTRIM(workspace_id) <> ''
            AND BTRIM(project_id) <> ''
            AND BTRIM(service_id) <> ''
            AND CHAR_LENGTH(workspace_id) <= 255
            AND CHAR_LENGTH(project_id) <= 255
            AND CHAR_LENGTH(service_id) <= 255
        ),
    CONSTRAINT webhook_delivery_targets_source_ids_chk
        CHECK (
            installation_id > 0
            AND repository_id > 0
            AND installation_grant_resource_version >= 1
            AND repository_grant_resource_version >= 1
            AND link_resource_version >= 1
        ),
    CONSTRAINT webhook_delivery_targets_repository_chk
        CHECK (
            BTRIM(repository_full_name) = repository_full_name
            AND repository_full_name = LOWER(repository_full_name)
            AND repository_full_name ~ '^[^/[:space:]]+/[^/[:space:]]+$'
            AND CHAR_LENGTH(repository_full_name) <= 512
        ),
    CONSTRAINT webhook_delivery_targets_branch_chk
        CHECK (
            BTRIM(branch) <> ''
            AND branch !~ '^refs/'
            AND branch !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(branch) <= 255
            AND ref = 'refs/heads/' || branch
        ),
    CONSTRAINT webhook_delivery_targets_commit_chk
        CHECK (
            commit_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'
            AND commit_sha !~ '^0+$'
        ),
    CONSTRAINT webhook_delivery_targets_clone_url_chk
        CHECK (
            clone_url ~ '^https://[^[:space:]@/?#]+/[^[:space:]?#]+\.git$'
            AND CHAR_LENGTH(clone_url) <= 2048
        ),
    CONSTRAINT webhook_delivery_targets_paths_chk
        CHECK (
            root_directory !~ '^/'
            AND root_directory !~ '(^|/)\.\.(/|$)'
            AND CHAR_LENGTH(root_directory) <= 1024
            AND config_path !~ '^/'
            AND config_path !~ '(^|/)\.\.(/|$)'
            AND CHAR_LENGTH(config_path) <= 1024
        ),
    CONSTRAINT webhook_delivery_targets_trigger_chk
        CHECK (
            BTRIM(trigger_reason) <> ''
            AND trigger_reason !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(trigger_reason) <= 128
        ),
    CONSTRAINT webhook_delivery_targets_temporal_chk
        CHECK (
            installation_grant_created_at <= installation_grant_updated_at
            AND installation_grant_updated_at <= delivery_received_at
            AND repository_grant_created_at <= repository_grant_updated_at
            AND repository_grant_updated_at <= delivery_received_at
            AND repository_access_verified_at <= repository_grant_updated_at
            AND repository_access_verified_at <= delivery_received_at
            AND link_created_at <= link_updated_at
            AND link_updated_at <= delivery_received_at
            AND link_repository_access_verified_at <= link_updated_at
            AND link_repository_access_verified_at <= delivery_received_at
        ),
    CONSTRAINT webhook_delivery_targets_dispatch_payload_chk
        CHECK (
            dispatch_payload_sha256 ~ '^[0-9a-f]{64}$'
            AND (
                (
                    dispatch_payload IS NOT NULL
                    AND OCTET_LENGTH(dispatch_payload) BETWEEN 1 AND 262144
                )
                OR (
                    dispatch_payload IS NULL
                    AND state IN ('succeeded', 'ignored', 'dead_letter')
                )
            )
        ),
    CONSTRAINT webhook_delivery_targets_state_chk
        CHECK (
            state IN (
                'pending',
                'processing',
                'retry_wait',
                'succeeded',
                'ignored',
                'dead_letter'
            )
        ),
    CONSTRAINT webhook_delivery_targets_attempts_chk
        CHECK (
            attempt >= 0
            AND max_attempts BETWEEN 1 AND 64
            AND manual_retries >= 0
        ),
    CONSTRAINT webhook_delivery_targets_lease_chk
        CHECK (
            (
                state = 'processing'
                AND lease_token IS NOT NULL
                AND BTRIM(lease_token) <> ''
                AND CHAR_LENGTH(lease_token) <= 255
                AND lease_expires_at IS NOT NULL
            )
            OR (
                state <> 'processing'
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),
    CONSTRAINT webhook_delivery_targets_terminal_chk
        CHECK (
            (
                state IN ('succeeded', 'ignored', 'dead_letter')
                AND terminal_at IS NOT NULL
            )
            OR (
                state NOT IN ('succeeded', 'ignored', 'dead_letter')
                AND terminal_at IS NULL
            )
        ),
    CONSTRAINT webhook_delivery_targets_reason_chk
        CHECK (
            reason_code !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(reason_code) <= 128
            AND CHAR_LENGTH(last_error) <= 512
        ),
    CONSTRAINT webhook_delivery_targets_result_chk
        CHECK (
            (operation_id IS NULL OR CHAR_LENGTH(operation_id) <= 255)
            AND (deployment_id IS NULL OR CHAR_LENGTH(deployment_id) <= 255)
            AND (staged_outbox_id IS NULL OR CHAR_LENGTH(staged_outbox_id) <= 255)
        ),
    CONSTRAINT webhook_delivery_targets_timestamps_chk
        CHECK (
            created_at >= delivery_received_at
            AND updated_at >= created_at
            AND available_at >= delivery_received_at
            AND (terminal_at IS NULL OR terminal_at >= created_at)
            AND (
                lease_expires_at IS NULL
                OR lease_expires_at > updated_at
            )
        )
);

CREATE INDEX webhook_delivery_targets_due_idx
    ON webhook_delivery_targets (
        available_at,
        created_at,
        id
    )
    WHERE state IN ('pending', 'processing', 'retry_wait');

CREATE INDEX webhook_delivery_targets_delivery_state_idx
    ON webhook_delivery_targets (
        delivery_id,
        state,
        service_id,
        id
    );

CREATE INDEX webhook_delivery_targets_service_received_idx
    ON webhook_delivery_targets (
        service_id,
        delivery_received_at DESC,
        id
    );

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (50, 'durable_webhook_inbox', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
