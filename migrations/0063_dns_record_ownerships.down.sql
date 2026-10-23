DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM dns_record_ownerships LIMIT 1) THEN
        RAISE EXCEPTION
            'cannot downgrade dns_record_ownerships while Midnight-owned provider records exist; remove them through the reconciler first';
    END IF;
END
$$;

DROP INDEX IF EXISTS idx_dns_record_ownerships_owner;
DROP TABLE dns_record_ownerships;
DROP INDEX IF EXISTS idx_dns_record_owners_scope;
DROP TABLE dns_record_owners;

DELETE FROM schema_migrations
 WHERE version = 63 AND name = 'dns_record_ownerships';
