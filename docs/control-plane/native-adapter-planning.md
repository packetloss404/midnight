# Native Adapter Planning

Jenkins, Azure DevOps, and Bitbucket native adapters stay behind explicit demand signals.

`internal/integrations.PlanNativeAdapters` returns a planning contract for the three adapter families:

- `candidate` when at least one requested workspace or repository signal exists for the adapter.
- `deferred` when no demand signal exists.

This is intentionally not wired to webhook intake, OAuth/OIDC setup flows, deployment workers, or preview environments. GitLab remains covered by the existing preview-environment slice, and GitHub App work remains separate from this planning contract.
