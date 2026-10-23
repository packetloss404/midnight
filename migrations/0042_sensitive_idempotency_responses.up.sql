-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Allow explicitly sensitive idempotency responses (for example one-time
-- credential mint responses) to be replayed without persisting their status,
-- headers, or body in plaintext. The application encrypts the complete
-- response under a per-response DEK, wraps that DEK with the configured KEK,
-- and authenticates the immutable reservation fence as envelope AAD.

ALTER TABLE idempotency_keys
    ADD COLUMN response_storage TEXT NOT NULL DEFAULT 'plaintext-v1',
    ADD COLUMN response_kek_id TEXT,
    ADD COLUMN response_wrapped_dek BYTEA,
    ADD COLUMN response_ciphertext BYTEA,
    ADD COLUMN response_envelope_form TEXT,
    ADD COLUMN response_aad_sha256 TEXT;

ALTER TABLE idempotency_keys
    ADD CONSTRAINT idempotency_keys_response_storage_kind_chk CHECK (
        response_storage IN ('plaintext-v1', 'envelope-v1')
    ),
    ADD CONSTRAINT idempotency_keys_response_storage_shape_chk CHECK (
        (
            response_storage = 'plaintext-v1'
            AND response_kek_id IS NULL
            AND response_wrapped_dek IS NULL
            AND response_ciphertext IS NULL
            AND response_envelope_form IS NULL
            AND response_aad_sha256 IS NULL
        )
        OR
        (
            response_storage = 'envelope-v1'
            AND state IN ('SUCCEEDED', 'FAILED')
            AND response_status IS NULL
            AND response_body IS NULL
            AND response_headers IS NULL
            AND response_kek_id IS NOT NULL
            AND BTRIM(response_kek_id) <> ''
            AND response_wrapped_dek IS NOT NULL
            AND OCTET_LENGTH(response_wrapped_dek) > 0
            AND response_ciphertext IS NOT NULL
            AND OCTET_LENGTH(response_ciphertext) > 0
            AND response_envelope_form IS NOT NULL
            AND response_envelope_form = 'midnight-envelope-v1'
            AND response_aad_sha256 IS NOT NULL
            AND response_aad_sha256 ~ '^[0-9a-f]{64}$'
        )
    );
