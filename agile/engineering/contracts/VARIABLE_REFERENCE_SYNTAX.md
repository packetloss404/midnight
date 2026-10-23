# Variable Reference Templating Syntax

Status: Normative (Parity Review)
Date: 2026-04-25

## Overview

Midnight services declare configuration via `midnight.yaml` and via the dashboard. Variables can reference other variables across services within the same project/environment, shared variables, system variables, and sealed secrets. The reference templating syntax is **`${{ <scope>.<NAME> }}`** — a closed grammar, no string interpolation outside the brackets, no command execution, no nested expressions in v1.

## Grammar (PEG)

```
Reference   ← '${{' WS Scope '.' Identifier WS '}}'
Scope       ← Identifier
Identifier  ← [A-Za-z_] [A-Za-z0-9_-]*
WS          ← [ \t]*
EscapedRef  ← '$${{'   // literal '${{'
```

A literal `${{` in a value is escaped as `$${{`. Unmatched `${{` without a closing `}}` fails preflight with `VARIABLE_REFERENCE_MALFORMED`.

## Scopes

| Scope token | Meaning | Example |
|---|---|---|
| `<service-name>` | Variable on another service in the same project/environment | `${{ postgres.DATABASE_URL }}` |
| `shared` | Project-scoped shared variable | `${{ shared.API_BASE }}` |
| `system` | Platform-provided system variable | `${{ system.MIDNIGHT_PUBLIC_DOMAIN }}` |
| `secret` | Explicit reference to a sealed secret on the same service | `${{ secret.STRIPE_API_KEY }}` |
| `self` | Same service (rare; explicit aliasing) | `${{ self.PORT }}` |

A scope token that does not match an existing service or known scope fails preflight with `VARIABLE_REFERENCE_FAILED`.

## Resolution semantics

1. References are evaluated when a deployment snapshot is being resolved.
2. Each reference is replaced with the resolved value at snapshot time. The resolved value is **deep-copied** into the snapshot (per panel decision on snapshot self-containment).
3. Sealed/secret values resolve to encrypted blob references in the snapshot, decrypted only at injection time.
4. Cross-environment-tier references (dev→prod, prod→preview) are denied by default and emit `VARIABLE_REFERENCE_FORBIDDEN`.
5. The actor creating a reference must have read access on the referenced variable's scope at creation; the consuming service's deploy principal must have read access at every resolution.

## Cycle detection

References form a directed graph. The resolver performs cycle detection using DFS at preflight; cycles fail with `VARIABLE_REFERENCE_CYCLE` listing the cycle.

## Affected-service redeploy preview

Renaming or deleting a referenced variable triggers an "affected services" preview in the dashboard and CLI (`midnight variables affected --variable <id>`). Apply requires explicit confirmation; subsequent deployments of affected services pick up the change.

## Built-in system variables

```
MIDNIGHT_PROJECT_ID
MIDNIGHT_PROJECT_NAME
MIDNIGHT_ENVIRONMENT_ID
MIDNIGHT_ENVIRONMENT_NAME
MIDNIGHT_SERVICE_ID
MIDNIGHT_SERVICE_NAME
MIDNIGHT_DEPLOYMENT_ID
MIDNIGHT_REPLICA_ID
MIDNIGHT_PUBLIC_DOMAIN
MIDNIGHT_PRIVATE_DOMAIN
MIDNIGHT_REGION   (Phase 3+; absent in single-host)
PORT
```

System variables are read-only references; attempts to override emit `VARIABLE_REFERENCE_SYSTEM_OVERRIDE`.

## Templating in build vs runtime

- `runtimeVars` and `runtimeSecrets` references resolve at deployment-snapshot resolution.
- `buildVars` references resolve at build start; secrets are not exposed to build by default.
- `publicClientVars` references resolve at build for static-site service types only and pass the build-output secret-prefix scan (`BUILD_OUTPUT_SECRET_LEAK`).

## Examples

```yaml
services:
  postgres:
    type: database-template
    template: postgres
    # exposes DATABASE_URL via system bindings

  api:
    type: web
    variables:
      runtime:
        DATABASE_URL: ${{ postgres.DATABASE_URL }}
        SENTRY_DSN: ${{ shared.SENTRY_DSN }}
        BASE_URL: https://${{ system.MIDNIGHT_PUBLIC_DOMAIN }}
        STRIPE_KEY: ${{ secret.STRIPE_API_KEY }}
```

## Negative examples (rejected at preflight)

```yaml
# Cycle:
api.runtime.A: ${{ web.B }}
web.runtime.B: ${{ api.A }}    # VARIABLE_REFERENCE_CYCLE

# Unknown scope:
api.runtime.X: ${{ neighbor.Y }}    # VARIABLE_REFERENCE_FAILED (no service named "neighbor")

# Cross-tier:
preview.runtime.X: ${{ production.SECRET }}    # VARIABLE_REFERENCE_FORBIDDEN

# Malformed:
api.runtime.X: ${{ shared.A    # VARIABLE_REFERENCE_MALFORMED

# Nested (not supported in v1):
api.runtime.X: ${{ shared.${{ system.NS }} }}    # rejected as malformed
```

## Compatibility with Railway

The grammar is intentionally compatible with Railway's `${{ <Service>.<NAME> }}` syntax (case-insensitive scope name acceptance for migration; canonicalized to lowercase). The migration assistant for `railway.json` / `railway.toml` rewrites uppercase scope tokens to canonical form and surfaces a one-shot diff for review.

## Failure reason codes

- `VARIABLE_REFERENCE_FAILED`
- `VARIABLE_REFERENCE_CYCLE`
- `VARIABLE_REFERENCE_FORBIDDEN`
- `VARIABLE_REFERENCE_MALFORMED`
- `VARIABLE_REFERENCE_SYSTEM_OVERRIDE`
- `BUILD_OUTPUT_SECRET_LEAK` (existing)

## Cross-references

- `midnight-platform-plan.md` → `Variables, Secrets, RBAC, And Audit`
- `agile/engineering/contracts/API_ERROR_CODE_REGISTRY.md`
- `agile/features/FEATURE_DEPLOYMENT_STATE_MACHINE.md`
