// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package graphql implements Midnight's Railway-inspired GraphQL subset
// mounted at /graphql/v2.
//
// The schema uses familiar resource and field names for Project, Service,
// ServiceInstance, Deployment, Variable, Environment, Volume, and
// CustomDomain. It is not a drop-in Railway API: only fields backed by the
// Midnight REST service layer are operational, and unsupported mutations
// return a typed NOT_IMPLEMENTED capability error.
//
// # Resolver pattern
//
// Resolvers MUST delegate to the existing services in internal/api/*
// (and the package-level types Project / Service / DeploymentResource /
// VariableMeta etc.). No business logic is re-implemented here; the
// GraphQL layer is a typed facade that translates field selections into
// service-method calls. In particular:
//
//   - Project / Service reads -> api.ProjectStore / api.ServiceStore
//   - Deployment reads        -> api.OperationStore via
//     api.DeploymentResourcesForService
//   - Variable reads/writes   -> secrets.Vault (via DataStack.VariableStore)
//   - Environment reads       -> api.EnvironmentStore
//   - Supported mutations     -> the same API services, idempotency store,
//     and operation FSM used by REST handlers
//
// Mutations accept an optional idempotencyKey argument that is routed to
// the same idempotency.Store as the REST Idempotency-Key header so a
// retry across either surface replays the same operation.
//
// # Runtime
//
// The runtime parses queries with gqlparser/v2 (the same parser gqlgen
// uses under the hood) and executes them via a hand-rolled, type-safe
// dispatcher. This avoids the 5k+ line gqlgen-generated file while still
// validating queries against the schema. The gqlgen.yml in this directory
// documents the intended generated-runtime shape for the day we swap
// in the full gqlgen executor; see ADR-0017.
//
// # No subscriptions
//
// v1 is poll-only (matches Railway's public API). The schema intentionally
// has no Subscription root type. SSE / streaming lives on the REST surface
// for now.
package graphql
