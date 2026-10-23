-- Refuse a lossy downgrade. The old schema cannot represent more than one
-- route binding for a deployment.
DO $$
BEGIN
    IF EXISTS (
        SELECT deployment_id
          FROM deployment_route_intents
         GROUP BY deployment_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade multi_route_domain_resources: deployments with multiple route bindings exist';
    END IF;
END
$$;

-- The old schema has neither service-managed product identity nor
-- resource-version CAS. Refuse to drop both while tenant product domains
-- remain: re-upgrading could otherwise recreate a binding at version 1 and
-- make a pre-downgrade delete token current again. A compatible downgrade is
-- possible only when no service-managed binding or retirement history exists.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM service_domain_bindings) THEN
        RAISE EXCEPTION
            'cannot downgrade multi_route_domain_resources: service-managed domain bindings exist; convert live bindings before retrying, and preserve schema 0052 if product identities have been retired';
    END IF;
END
$$;

-- Retirement history is the durable ABA fence for deleted product domains.
-- Dropping and later recreating it would make a recovery-reused identity look
-- fresh, so operators must not cross the schema boundary while history exists.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM retired_product_domain_ids) THEN
        RAISE EXCEPTION
            'cannot downgrade multi_route_domain_resources: retired product domain identity history exists; preserve schema 0052 or restore from a pre-product-domain backup';
    END IF;
END
$$;

DROP INDEX IF EXISTS idx_deployment_route_intents_scope_deployment;

ALTER TABLE deployment_route_intents
    DROP CONSTRAINT deployment_route_intents_pkey;

ALTER TABLE deployment_route_intents
    ADD CONSTRAINT deployment_route_intents_pkey
    PRIMARY KEY (deployment_id);

DROP TABLE IF EXISTS service_domain_bindings;
DROP TABLE IF EXISTS retired_product_domain_ids;
DROP INDEX IF EXISTS idx_http_routes_id_domain_scope;
DROP INDEX IF EXISTS idx_domains_one_generated_per_service_environment;

ALTER TABLE domains
    DROP CONSTRAINT IF EXISTS domains_resource_version_chk;

ALTER TABLE domains
    DROP COLUMN IF EXISTS resource_version;

DELETE FROM schema_migrations
WHERE version = 52
  AND name = 'multi_route_domain_resources';
