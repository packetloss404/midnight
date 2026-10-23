-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
-- Durable, presentation-only topology coordinates scoped to one project/env.

CREATE TABLE topology_layouts (
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    nodes JSONB NOT NULL DEFAULT '[]'::jsonb,
    resource_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, environment_id),
    CONSTRAINT topology_layouts_project_scope_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE CASCADE,
    CONSTRAINT topology_layouts_environment_scope_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT topology_layouts_nodes_array_chk
        CHECK (jsonb_typeof(nodes) = 'array'),
    CONSTRAINT topology_layouts_nodes_limit_chk
        CHECK (jsonb_array_length(nodes) <= 500),
    CONSTRAINT topology_layouts_version_chk
        CHECK (resource_version > 0)
);

CREATE INDEX topology_layouts_workspace_idx
    ON topology_layouts (workspace_id, updated_at DESC, project_id, environment_id);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (58, 'topology_layouts', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
