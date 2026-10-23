-- 0072_instance_domain_settings.up.sql — persist the installation domain namespace.

CREATE TABLE instance_domain_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    root_domain TEXT NOT NULL,
    console_hostname TEXT NOT NULL,
    apps_domain TEXT NOT NULL,
    ingress_target TEXT NOT NULL DEFAULT '',
    resource_version BIGINT NOT NULL DEFAULT 1 CHECK (resource_version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (root_domain = lower(root_domain)),
    CHECK (console_hostname = lower(console_hostname)),
    CHECK (apps_domain = lower(apps_domain))
);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (72, 'instance_domain_settings', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
