DELETE FROM schema_migrations WHERE version = 26;

DROP TABLE IF EXISTS domain_certificate_orders;
DROP TABLE IF EXISTS domain_dns_challenges;
DROP TABLE IF EXISTS route_publication_observations;
DROP TABLE IF EXISTS ingress_environment_enrollments;

ALTER TABLE route_controller_groups
    DROP CONSTRAINT route_controller_groups_apply_state_chk;

ALTER TABLE route_controller_groups
    ADD CONSTRAINT route_controller_groups_apply_state_chk CHECK (
        apply_state IN (
            'pending', 'private', 'blocked_tls', 'blocked_backend',
            'file_applied', 'failed'
        )
    );
