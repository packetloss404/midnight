-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Refuse to silently discard a still-replayable encrypted response. Expiry
-- destroys envelope material first, after which rollback is lossless.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM idempotency_keys
        WHERE response_storage = 'envelope-v1'
    ) THEN
        RAISE EXCEPTION
            'cannot roll back sensitive idempotency responses while encrypted replay history exists';
    END IF;
END
$$;

ALTER TABLE idempotency_keys
    DROP CONSTRAINT IF EXISTS idempotency_keys_response_storage_shape_chk,
    DROP CONSTRAINT IF EXISTS idempotency_keys_response_storage_kind_chk,
    DROP COLUMN IF EXISTS response_aad_sha256,
    DROP COLUMN IF EXISTS response_envelope_form,
    DROP COLUMN IF EXISTS response_ciphertext,
    DROP COLUMN IF EXISTS response_wrapped_dek,
    DROP COLUMN IF EXISTS response_kek_id,
    DROP COLUMN IF EXISTS response_storage;

