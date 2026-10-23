// Package github implements the Lane 4 slice of the GitHub App
// integration: the App-manifest registration data, the push-event
// handler that promotes a GitHub push to a Midnight deploy operation,
// and the required-CI status-check policy hook used by the promotion
// gate.
//
// Panel decision pinning (Sprint 10 GH-1005, GH-1006):
//
//   - "Operator-owned shared GitHub App for self-hosted instance and
//     user-provided GitHub App credentials for private installs."
//   - "Required status-check gate support."
//   - "Untrusted-build classification (fork PR, non-member commit) is
//     enforced upstream of build."
//
// Lane 4 ships:
//
//   - The GitHub App manifest JSON Lane 1 / installer wraps with the
//     operator's callback URL (Manifest, RenderManifest).
//   - A pure-Go push-event parser (PushEvent, ParsePushEvent) that
//     surfaces the panel-locked fork/branch/sha fields the deploy
//     operation needs.
//   - A small required-status policy hook (StatusChecker /
//     RequiredCheckPolicy) the promotion gate consults before
//     advancing DEPLOYING -> ACTIVE.
//
// Live calls to the GitHub API are gated behind the build tag
// "midnightlivegithub". Without that tag the StatusChecker returns
// ErrLiveGitHubDisabled. Tests use a stub StatusChecker.
//
// The GitHub App auto-deploy slice (ADR 0022) adds:
//
//   - AppClient (app.go): mints a short-lived RS256 JWT from the
//     operator's RSA private key and exchanges it for an installation
//     token via /app/installations/{id}/access_tokens.
//   - WebhookVerifier (webhook.go): HMAC-SHA256 verifies X-Hub-
//     Signature-256 against the operator-configured secret.
//   - Router (router.go): turns one inbound push event into zero or
//     more DeployIntents by consulting a ServiceLookup that resolves
//     `(repo, branch)` to linked services.
//   - ServiceLink (link.go): the panel-locked shape Midnight persists
//     under `service.spec.github`.
package github
