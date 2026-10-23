-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Replace the global, mutable idempotency-key row with immutable attempts.
-- A client key is scoped by actor + tenant + method + canonical route. Every
-- attempt has a generation fence. Expiry transitions an attempt to EXPIRED;
-- history is retained so a late request cannot mutate a replacement attempt.

ALTER TABLE idempotency_keys
    ADD COLUMN tenant TEXT,
    ADD COLUMN generation TEXT;

-- Pre-tenant records belonged to the installation-wide compatibility
-- principal. Keep valid records replayable in that explicit namespace.
UPDATE idempotency_keys
SET actor = 'legacy:operator'
WHERE BTRIM(actor) = '';

UPDATE idempotency_keys
SET tenant = 'installation:default'
WHERE tenant IS NULL OR BTRIM(tenant) = '';

-- The legacy table had a global key primary key, so this deterministic value
-- is unique for every existing attempt. New rows use a random UUID default or
-- the application's cryptographically random 128-bit generation.
UPDATE idempotency_keys
SET generation = 'legacy-' || md5(
    actor || E'\x1f' ||
    tenant || E'\x1f' ||
    method || E'\x1f' ||
    path_template || E'\x1f' ||
    key || E'\x1f' ||
    created_at::TEXT
)
WHERE generation IS NULL OR BTRIM(generation) = '';

-- Rows lacking method/path were never safely replayable because their scope
-- collapsed unrelated requests. Preserve them as expired forensic history
-- under a deterministic non-empty legacy scope.
UPDATE idempotency_keys
SET state = 'EXPIRED',
    method = CASE WHEN BTRIM(method) = '' THEN 'LEGACY' ELSE method END,
    path_template = CASE
        WHEN BTRIM(path_template) = ''
        THEN '/legacy/idempotency/' || md5(key)
        ELSE path_template
    END,
    updated_at = CURRENT_TIMESTAMP
WHERE BTRIM(method) = '' OR BTRIM(path_template) = '';

ALTER TABLE idempotency_keys
    ALTER COLUMN actor SET DEFAULT 'legacy:operator',
    ALTER COLUMN tenant SET DEFAULT 'installation:default',
    ALTER COLUMN tenant SET NOT NULL,
    ALTER COLUMN generation
        SET DEFAULT REPLACE(gen_random_uuid()::TEXT, '-', ''),
    ALTER COLUMN generation SET NOT NULL;

ALTER TABLE idempotency_keys
    DROP CONSTRAINT idempotency_keys_pkey;

ALTER TABLE idempotency_keys
    ADD CONSTRAINT idempotency_keys_pkey
        PRIMARY KEY (
            actor,
            tenant,
            method,
            path_template,
            key,
            generation
        ),
    ADD CONSTRAINT idempotency_keys_scope_nonempty_chk CHECK (
        BTRIM(actor) <> ''
        AND BTRIM(tenant) <> ''
        AND BTRIM(method) <> ''
        AND BTRIM(path_template) <> ''
        AND BTRIM(generation) <> ''
    ),
    ADD CONSTRAINT idempotency_keys_state_chk CHECK (
        state IN ('RESERVED','RUNNING','SUCCEEDED','FAILED','EXPIRED')
    );

-- Exactly one non-expired attempt may own a logical scope. EXPIRED rows are
-- deliberately excluded so a new generation can start without deleting
-- history.
CREATE UNIQUE INDEX idempotency_keys_active_scope_uq
    ON idempotency_keys (actor, tenant, method, path_template, key)
    WHERE state IN ('RESERVED','RUNNING','SUCCEEDED','FAILED');

CREATE INDEX idempotency_keys_scope_history_idx
    ON idempotency_keys (
        actor,
        tenant,
        method,
        path_template,
        key,
        created_at DESC
    );

CREATE INDEX idempotency_keys_key_idx
    ON idempotency_keys (key);
