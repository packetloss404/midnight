-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- The legacy schema can represent only one row for a raw client key. Refuse
-- downgrade whenever actor/tenant scopes or retained generations would
-- collide; silently choosing or deleting an attempt would destroy replay and
-- forensic evidence.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM idempotency_keys
        WHERE actor <> 'legacy:operator'
           OR tenant <> 'installation:default'
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade scoped idempotency: non-legacy actor or tenant attempts exist';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM idempotency_keys
        GROUP BY key
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade scoped idempotency: multiple attempts exist for at least one client key';
    END IF;
END
$$;

DROP INDEX IF EXISTS idempotency_keys_key_idx;
DROP INDEX IF EXISTS idempotency_keys_scope_history_idx;
DROP INDEX IF EXISTS idempotency_keys_active_scope_uq;

ALTER TABLE idempotency_keys
    DROP CONSTRAINT IF EXISTS idempotency_keys_state_chk,
    DROP CONSTRAINT IF EXISTS idempotency_keys_scope_nonempty_chk,
    DROP CONSTRAINT idempotency_keys_pkey;

ALTER TABLE idempotency_keys
    DROP COLUMN generation,
    DROP COLUMN tenant;

ALTER TABLE idempotency_keys
    ALTER COLUMN actor SET DEFAULT '',
    ADD CONSTRAINT idempotency_keys_pkey PRIMARY KEY (key);
