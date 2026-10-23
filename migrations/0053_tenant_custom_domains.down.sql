DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM service_domain_bindings b
          JOIN domains d ON d.id = b.domain_id
         WHERE d.kind = 'custom'
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade tenant_custom_domains: tenant custom domain bindings exist';
    END IF;
END
$$;

DROP INDEX IF EXISTS idx_domain_dns_challenges_pending;
ALTER TABLE domain_dns_challenges DROP COLUMN IF EXISTS verified_at;

DELETE FROM schema_migrations
 WHERE version = 53 AND name = 'tenant_custom_domains';
