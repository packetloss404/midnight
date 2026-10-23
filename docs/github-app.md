# GitHub App: grant, link, and auto-deploy

Midnight uses an operator-owned GitHub App for private-repository discovery,
signed push webhooks, and short-lived checkout credentials. There is no central
Midnight service with access to your repositories.

The normal product flow separates two roles:

- a workspace administrator starts the App installation from Project Settings
  (or the CLI) and signs in to GitHub; and
- a workspace member with both `service.write` and
  `deployment.trigger.git` links one granted repository and branch to a
  service.

Ordinary users never enumerate App-global installations. The dashboard and CLI
show only repositories granted to the selected project's workspace.

## 1. Register the App

GitHub supports a [manifest-based registration flow][gh-manifest]. Midnight
ships `deploy/github-app-manifest.yaml`; render it with the instance URL and a
stable webhook endpoint ID, then use GitHub's App-registration flow:

```bash
yq -o=json eval . deploy/github-app-manifest.yaml > /tmp/midnight-app.json
sed -i "s|https://midnight.example.com|$MIDNIGHT_URL|g" /tmp/midnight-app.json
sed -i "s|{ENDPOINT_ID}|$WEBHOOK_ENDPOINT_ID|g" /tmp/midnight-app.json
```

The shipped manifest enables **Request user authorization (OAuth) during
installation**, sets the setup URL to
`/api/v1alpha/github/setup/callback`, and registers that same URL as an OAuth
callback. Preserve those values if the App is configured manually or edited in
GitHub later.

After App registration redirects with its one-time manifest code, exchange
that code directly with GitHub:

```text
POST https://api.github.com/app-manifests/{code}/conversions
```

Store the returned App ID, client ID, client secret, webhook secret, and private
key on the Midnight installation. Record the App slug from its public App URL
(`/apps/{slug}`). The registration redirect is distinct from the installation
setup callback; Midnight handles only the latter.

## 2. Configure Midnight

| Variable | Process | Description |
|---|---|---|
| `MIDNIGHT_GITHUB_APP_ID` | server + build worker + status worker | Numeric App ID. |
| `MIDNIGHT_GITHUB_PRIVATE_KEY_PATH` | server + build worker + status worker | Read-only path to the App PEM. |
| `MIDNIGHT_GITHUB_WEBHOOK_SECRET` | server only | GitHub webhook HMAC secret. |
| `MIDNIGHT_GITHUB_WEBHOOK_ENDPOINT_ID` | server only | Stable inbound path ID; defaults to `github`. |
| `MIDNIGHT_GITHUB_API_BASE_URL` | server + build worker + status worker | Optional GHES API override; defaults to `https://api.github.com`. |
| `MIDNIGHT_GITHUB_CLIENT_ID` | server only | App OAuth client ID used to verify the installing GitHub user. |
| `MIDNIGHT_GITHUB_CLIENT_SECRET` | server only | App OAuth client secret; never sent to the dashboard or persisted by the setup flow. |
| `MIDNIGHT_GITHUB_APP_SLUG` | server only | Lowercase slug from `https://github.com/apps/{slug}`. |
| `MIDNIGHT_GITHUB_CALLBACK_URL` | server only | Exact public HTTPS URL ending in `/api/v1alpha/github/setup/callback`. |
| `MIDNIGHT_GITHUB_WEB_BASE_URL` | server only | GHES browser origin; omit for public GitHub. |
| `MIDNIGHT_GITHUB_CLONE_HOST` | build worker | Exact host allowed to receive checkout credentials; defaults to `github.com`. |
| `MIDNIGHT_GITHUB_STATUS_POLL_INTERVAL` | status worker | Optional idle poll interval; defaults to `5s`. |
| `MIDNIGHT_GITHUB_STATUS_BATCH_SIZE` | status worker | Optional claim batch size; defaults to `25`. |
| `MIDNIGHT_GITHUB_STATUS_LEASE_DURATION` | status worker | Optional per-attempt lease; defaults to `30s`. |
| `MIDNIGHT_API_BEARER_TOKEN` | server/operator | Installation-wide recovery credential used only for protected operator routes. |

The server loads the GitHub fields at boot. A partial core App configuration,
or any setup variable without the complete setup set, fails startup. The
callback must be absolute HTTPS, have no query string, and target the exact
Midnight callback path. When every App field is absent, GitHub product
discovery reports that the integration is unavailable; public-repository
builds remain independent.

The App must retain `statuses:write`; the shipped manifest requests it. Enable
`midnight-github-status-worker.service` only after the App ID, private key, and
API base are configured. The outbound worker intentionally does not receive or
require the webhook HMAC secret.

## 3. Install and grant repositories

In Project Settings, a workspace administrator selects **Install GitHub App**.
The equivalent CLI command is:

```bash
midnight github setup --workspace-id wrk_123
```

The command prints a short-lived GitHub URL. Open it, select the organization
or account and exact repositories, authorize the App, and let GitHub return to
Midnight. REST clients can start the same empty-body operation:

```bash
curl -X POST \
  -H "Authorization: Bearer $MIDNIGHT_HUMAN_SESSION" \
  "https://midnight.example.com/api/v1alpha/workspaces/wrk_123/github/setup"
```

Midnight signs an opaque state containing only a random nonce and timestamps;
PostgreSQL binds its nonce digest to the exact workspace, user, and active
session. State expires after ten minutes and is consumed once. At callback time
Midnight:

1. rejects duplicate or unknown query fields, including caller-supplied owner
   or workspace values;
2. rechecks the initiating human session and workspace-admin membership;
3. exchanges GitHub's one-time code for an in-memory user access token and
   proves that user can access the exact installation;
4. independently resolves the installation and every accessible repository
   through the App API; and
5. atomically synchronizes grants, unlinks services whose repository access
   was removed, consumes state, and appends audit evidence.

OAuth codes, user access tokens, signed state, and App secrets are not stored.
The browser returns to Midnight only after the durable transaction commits.

### Break-glass recovery assignment

If browser OAuth is unavailable, an installation-wide recovery operator can
still assign an already installed App and complete repository-ID allowlist to
one workspace:

```bash
curl -X PUT \
  -H "Authorization: Bearer $MIDNIGHT_OPERATOR_TOKEN" \
  -H "Idempotency-Key: github-grant-42-v1" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_123","repository_ids":[11,12]}' \
  "https://midnight.example.com/api/v1alpha/github/installations/42/workspace-grant"
```

The recovery request remains a compatibility and break-glass surface, not the
ordinary dashboard path. The server obtains account and repository metadata
from GitHub; those fields cannot be supplied by the caller. An installation
belongs to at most one workspace. Replacing the list revokes omitted repository
grants and immediately unlinks services using them.

Inspect or revoke the grant:

```bash
curl \
  -H "Authorization: Bearer $MIDNIGHT_OPERATOR_TOKEN" \
  "https://midnight.example.com/api/v1alpha/github/installations/42/workspace-grant?workspace_id=ws_123"

curl -X DELETE \
  -H "Authorization: Bearer $MIDNIGHT_OPERATOR_TOKEN" \
  -H "Idempotency-Key: github-grant-42-revoke-v1" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_123"}' \
  "https://midnight.example.com/api/v1alpha/github/installations/42/workspace-grant"
```

Migration 0049 intentionally quarantines and deactivates legacy
`service.spec.github` links. Re-grant and relink those services after upgrade;
Midnight does not infer tenant authority from old JSON metadata.

## 4. Link a service

The dashboard's service source panel performs this flow with the current human
session. The CLI exposes the same project-scoped discovery and strict link:

```bash
midnight service github repositories --project-id prj_123
midnight service github branches --project-id prj_123 --repository-id 11
midnight service github link svc_123 \
  --installation-id 42 \
  --repository-id 11 \
  --branch main
midnight service github get svc_123
midnight service github unlink svc_123
```

Equivalent human-session REST routes are:

```text
GET    /api/v1alpha/projects/{project_id}/github/repositories
GET    /api/v1alpha/projects/{project_id}/github/repositories/{repository_id}/branches
GET    /api/v1alpha/services/{service_id}/github
PUT    /api/v1alpha/services/{service_id}/github
DELETE /api/v1alpha/services/{service_id}/github
```

`PUT` requires `Idempotency-Key` and accepts exactly:

```json
{
  "installation_id": 42,
  "repository_id": 11,
  "branch": "main"
}
```

The server verifies the exact repository and branch live, rechecks current
membership and both required capabilities, then commits the link, idempotent
response, and audit row in one serializable transaction. Linking does not
trigger a deployment. `wait_for_ci` and required-check configuration are
rejected until a production check gate is admitted.

## 5. Verify push intake

For a trusted push to the linked branch Midnight:

1. accepts only the configured GitHub provider and exact webhook path;
2. bounds the request body and verifies the GitHub HMAC before parsing it;
3. requires an exact delivery ID, event type, installation ID, repository ID,
   canonical repository name, branch, and 40-character commit SHA;
4. looks up canonical links using those exact coordinates;
5. skips fork/untrusted pushes;
6. revalidates the current installation grant, repository grant, link, and link
   version under the same installation lock used by revocation before durable
   deployment/outbox work is admitted; and
7. queues a non-secret GitHub source descriptor.

Immediately before checkout, the build worker mints a short-lived token
restricted to the exact repository ID and `contents:read`. It revalidates the
workspace, project, service, installation, repository, branch, and exact link
resource version before minting, again before resolving build variables, and
again immediately before the build solve. The token is injected through an
environment-only Git HTTP header and is never persisted in a URL, operation,
event, audit row, job payload, or evidence bundle.

### Current delivery boundary

The signed receiver commits the complete verified body and acceptance audit
before returning `202`. An exact delivery/envelope replay returns `200`;
reusing the same provider identity with different immutable metadata returns
`409`. PostgreSQL workers plan only against source authority that existed no
later than the database receipt timestamp, persist one immutable target per
linked service, and use fenced delivery/target leases with bounded retry and
dead-letter state. Canonical staged intake remains independently idempotent if
a process exits after committing build work but before completing the inbox
target.

This is **at-least-once** delivery, not exactly once. Lease expiry may repeat a
downstream attempt. Recovery operators can inspect metadata-only state and
atomically redrive a dead letter through:

```text
GET  /api/v1alpha/webhook-deliveries
GET  /api/v1alpha/webhook-deliveries/{delivery_id}
POST /api/v1alpha/webhook-deliveries/{delivery_id}/redrive
```

The redrive request has an exactly empty body and requires
`Idempotency-Key`. Retained bodies, target dispatch payloads, signatures,
key identifiers, and lease tokens are never returned by these routes.
Terminal payload bytes age out after 30 days while metadata and hashes remain;
an aged-out dead letter can be inspected but not redriven.

## Troubleshooting

| Symptom | Meaning |
|---|---|
| `401 WEBHOOK_SIGNATURE_INVALID` | The request body does not match the configured GitHub webhook secret. |
| `404 WEBHOOK_ENDPOINT_UNKNOWN` | The stable endpoint ID is not configured on this instance. |
| `409 WEBHOOK_DELIVERY_IDENTITY_MISMATCH` | The same GitHub delivery ID was presented with different immutable envelope data; investigate the sender and retain the first accepted body. |
| `503 WEBHOOK_INBOX_UNAVAILABLE` | The verified delivery could not be committed; restore PostgreSQL and allow GitHub to redeliver. |
| Setup returns `403` | The active human session is not a workspace administrator, or that authority changed before callback. |
| Callback returns `400` | State expired, callback fields were altered/duplicated, or the callback URL/App settings do not match. Start a new setup. |
| Callback returns `409` | State was already consumed, or the installation belongs to another workspace. |
| Callback returns `502` | GitHub did not verify the OAuth user, installation, or exhaustive repository set. |
| Workspace-grant `409` | That installation is already assigned to another workspace. |
| No repositories in the product selector | The App has not been installed for this workspace/repository, or the current member cannot access the project. |
| Link returns `403` | The current member lacks `service.write` or `deployment.trigger.git`. |
| Link returns `502` | GitHub did not verify the installation/repository/branch at that moment. |
| Push creates no deployment | The link no longer matches the exact installation/repository/name/branch, the push is untrusted, or authority was revoked before durable intake. |

## Security notes

- The App private key is needed by the server and build worker; the webhook
  secret is server-only.
- Product requests cannot provide repository names or clone URLs.
- App-global installation/repository enumeration and manual grant assignment
  remain operator compatibility surfaces; the normal dashboard/CLI setup uses
  workspace-bound one-time state and GitHub user proof.
- Repository access is granted by numeric ID and checkout tokens are
  repository-scoped with only `contents:read`.
- Revocation and staged GitHub intake share an installation advisory lock.
- Pending build jobs must still pass current-link/version checks before token
  mint, variable resolution, and build solve. A build that has already crossed
  its final authority safe point is not synchronously interrupted.
- Migration 0050 retains verified request bodies for durable retry, but no
  signature bytes or signing secret. Operator list/detail/audit surfaces are
  metadata-only, and a daily bounded pass purges terminal payload bytes after
  30 days while retaining their hashes and outcomes.

## Related

- ADR 0022 — original GitHub App and push-routing shape.
- ADR 0038 — canonical tenant-safe GitHub source links.
- ADR 0039 — durable at-least-once verified-webhook inbox.
- ADR 0043 — workspace-bound GitHub App setup and callback proof.
- Migration 0049 — workspace installation grants, repository grants, canonical
  service links, and legacy quarantine.
- Migration 0050 — retained verified deliveries, immutable targets, leases,
  retry/dead-letter state, and recovery metadata.
- `internal/integrations/github/` — App client, verifier, source-link store, and
  push router.
- `internal/build/source_checkout.go` — credential-safe Git transport.

[gh-manifest]: https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-using-url-parameters
