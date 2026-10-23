-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

DROP TABLE IF EXISTS topology_layouts;
DELETE FROM schema_migrations WHERE version = 58 AND name = 'topology_layouts';
