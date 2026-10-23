-- 0053 — tenant-scoped custom hostname ownership evidence.
--
-- Product custom hostnames reuse the durable domain/challenge tables but are
-- admitted only through the nested human-session API.  Recovery-created
-- custom rows are never silently adopted: a service_domain_bindings row is
-- the product ownership marker, and the challenge must be verified before a
-- public route or deployment binding is eligible.
ALTER TABLE domain_dns_challenges
    ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_domain_dns_challenges_pending
    ON domain_dns_challenges (updated_at, domain_id)
    WHERE state <> 'verified';

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (53, 'tenant_custom_domains', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
