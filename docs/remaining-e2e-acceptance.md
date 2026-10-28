# Credential-ready external acceptance

The external lanes in `scripts/acceptance` are executable acceptance mechanisms.
Their local contract tests do **not** establish live Anthropic, OpenAI, MiniMax,
GitHub, DNS, or ACME success. No external credential was available when these
lanes were added. A release must retain each actual execution receipt and its
exact candidate commit before changing an external feature's verification
status in [the feature ledger](feature-verification.md).

The runners use the real product HTTP APIs for chat, mutation authority,
deployment, diagnosis, and GitHub-derived deployment state. The DNS lane calls
the native issuer and production DNS HTTP adapter directly; it has a narrower
proof boundary than an installed domain lifecycle.

## Prepare a disposable target

Build on Linux with the repository's pinned Go version:

```sh
go build -trimpath -o /tmp/midnight-live-acceptance ./scripts/acceptance
go test -count=1 ./scripts/acceptance
```

Copy the relevant `scripts/acceptance/*.env.example` outside Git. Use a private
directory (`0700`) and literal configuration/credential files (`0600`). The
command refuses Windows, symlinks, nonregular files, group/world access,
oversized files, unknown keys, and duplicate entries. Values are literal
`KEY=value`; the file is never sourced as shell code. Relative credential paths
resolve beside that file. Ambient credentials and provider keys are not read by
the acceptance command. File owners and nonsymlink directory ancestry must be
the current Unix user or root; immediate credential/config parents must be
owner-only. Group/world-writable ancestors are refused, with a trusted sticky
ancestor such as `/tmp` allowed above a private owned child. Residual journals
use that same checked private directory boundary. The DNS child receives a
validated configuration snapshot with absolute credential paths instead of
reopening the original configuration after preflight. The QA server/worker holds
its own provider credentials.

For product lanes, use a real, short-lived human session belonging to an isolated
QA account. The AI agent and diagnosis lanes specifically require a verified
**installation administrator** human session and a dedicated disposable QA
installation containing only the pinned QA resources. Current canonical
conversation routes admit human installation administrators (or the emergency
operator credential); ordinary tenant sessions receive 403. The runner never
substitutes an operator credential. It checks `instance_admin=true` and performs
a read-only canonical conversation collection request before any paid call or
deployment intake. Administrator conversation visibility is installation-wide:
production does not wire a tenant `ConversationScopeAuthorizer`. Ordinary tenant
conversation RBAC and cross-tenant isolation remain explicitly unverified.

The account must see exactly the pinned project, and the project must contain
exactly the pinned service and environment. The runner verifies
`/compat`'s exact 40-character build commit, `/auth/me`, workspace/project
lineage, and collection contents. It rejects an HTTP API base except loopback;
remote APIs require HTTPS. It refuses redirects so credentials cannot follow a
redirect to another origin. Production targets are outside this contract.

```sh
/tmp/midnight-live-acceptance --lane agent --env-file /private/qa/agent.env
# Expected exit 3: READY_NOT_EXECUTED, after read-only preflight.

# Run only after the operator has authorized the configured paid calls/writes.
/tmp/midnight-live-acceptance --lane agent --env-file /private/qa/agent.env --execute
```

Do not put tokens, private keys, plaintext prompts, or provider responses in
command arguments. Disable shell tracing around credential preparation. Keep
configuration files and captured output outside tracked/public artifacts; the
configuration itself contains private scope identifiers. Example hostnames and
IDs here are placeholders.

Exit status is part of the gate:

- `0`, `PASSED`: executed checks and verified cleanup succeeded for that lane.
- `3`, `READY_NOT_EXECUTED`: preflight succeeded; no live mutation was attempted.
- `78`, `BLOCKED`: credentials, authority, scope, configuration, or capability
  prerequisites are absent. This is never a skip or a pass.
- `1`, `FAILED`: an executed check, deadline, remote response, or cleanup failed.
- `64`: invalid arguments. Unknown arguments are not echoed.

Receipts contain fixed check/reason codes, a random run ID, timestamps, the
candidate commit and declared provider where applicable, a scope digest, cleanup
status, retained evidence/resource kinds, and explicit unverified areas.
They omit credentials, endpoints, repository/domain names, raw external error
bodies, transcripts, and resource IDs. A receipt is a local test result, not a
signed attestation of provider identity. Keep the private configuration needed
to associate its scope digest with the actual QA resources.

Each execution has a configured 15–1800 second deadline. Cleanup uses a separate
90 second budget (three minutes for GitHub). Thus the maximum command duration
includes cleanup. Cancellation/timeout cannot become a pass. A forced process
kill or host loss can prevent cleanup; use the run ID and private scope file to
inspect residual QA resources. `CLEANUP_UNVERIFIED` requires operator review.

## In-product provider chat and mutation authority

Run `--lane agent` separately against QA configurations for `anthropic`, `openai`,
and `minimax`, using an explicit provider-supported `MODEL` for each. The
`PROVIDER` field declares the expected server configuration; the product API
does not independently attest its upstream vendor. Review the QA server's
`MIDNIGHT_AGENT_PROVIDER`, provider endpoint/key, and model configuration for
every run. A proxy or fake provider endpoint cannot establish real vendor proof.
The runner never treats its own declaration or an adapter-only request as
installed provider evidence.

The server must permit agent mutation for the disposable scope. Retain server
token/cost budgets and configure a bounded generation cap; the HTTP harness
does not independently impose a dollar cap on a remote model. The lane makes
three product conversation sends; each send may execute multiple provider
iterations. Anthropic, OpenAI, and MiniMax credentials can incur charges. The
`ACK_PAID_CALLS=allow-bounded-calls` and
`ACK_EXTERNAL_MUTATIONS=disposable-scopes-only` values are required for execution.

The actual journey is:

1. Create a scoped product conversation, request `get_service`, and require one
   correctly scoped tool call, a correlated successful result, final text, and
   nonzero model usage from the real `assistant_message` response.
2. Request `set_variable` with per-message mutations disabled. Require no tool
   call/result and an unchanged empty QA variable collection.
3. Request the same unique nonsecret QA variable with mutations enabled. Require
   the exact service/environment/name/value tool input, a correlated successful
   result, version-one tenant metadata, and a project audit row with a row hash.
4. Delete only that run's exact version-one QA variable using both the expected
   version body and `If-Match`, delete its conversations, and read back absence.

An unrelated, rotated, or changed variable blocks cleanup rather than authorizing
its deletion. Creation retries use the original idempotency key/body to recover
a lost response. Audit rows and idempotency replay records remain as history.
The check verifies administrator authentication and mutation results through the
installed product endpoints; variable tools still delegate to the existing
tenant mutation coordinator under the authenticated human context. It does not
claim ordinary tenant chat authority or conversation isolation, inspect encrypted
database bytes, or exercise a browser.

Official tool-loop contracts explain why a final sentence alone is insufficient:
the application must execute the call and correlate its result before the model
continues. See [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling),
[Anthropic tool-call handling](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls),
and [MiniMax's Anthropic-compatible interface](https://platform.minimax.io/docs/api-reference/text-anthropic-api).

## Worker diagnosis

Use `--lane diagnosis` with the common provider scope and `DIAGNOSIS_IMAGE`, an
operator-provided digest-pinned QA image that fails during a real deployment.
The worker image policy must allow that exact image. The service/environment
must have no active deployment and no generated/custom domain bindings; the
request supplies no public route. Enable and configure the real diagnosis
worker/provider/model on that QA installation before execution.

The lane submits the real deployment intake, waits for `FAILED`, checks canonical
operation/deployment/image lineage, then waits for the worker's
`deploy:diagnosis_attached` event. It fetches the actual persisted diagnosis
conversation and report, requiring the configured model and matching service,
operation, and deployment tags. It does not use the synthetic `/fail` endpoint.
Removal of the failed candidate must complete through the actual action API.
Failed deployment/operation history, the diagnosis conversation, and audit rows
remain as evidence.

A diagnosis report stored by the worker does not expose provider usage or its
internal tool transcript. This lane proves the installed failure-to-report
workflow under the reviewed worker configuration, not an independently observed
diagnosis tool loop or budget limit. The agent lane separately checks the
in-product chat tool loop. Unsupported/unconfigured workers cannot be made green
by changing only `PROVIDER` in the acceptance file.

## GitHub App push, status, and preview lifecycle

Use `--lane github` only with a pre-existing product GitHub App installation,
verified human workspace installation grant, private disposable repository, and
service source link bound to `GITHUB_QA_BRANCH`. The branch name must start with
`qa-midnight-`; `GITHUB_EXPECTED_HEAD` pins its current commit. The repository
must contain a working buildable service. Real GitHub webhooks must reach the QA
installation, with workers and registry configured for successful builds.
The runner reads the human-scoped service link and preview collection routes;
these support ordinary authorized tenant humans and do not use the global
administrator-only `/github/installations` route. Current membership, workspace
grant, and source-link authorization remain requirements of the real coordinator.

Two credentials have different purposes:

- The product App private key verifies the configured App/installation and mints
  an ephemeral read-only token limited to the exact repository. JWT-authenticated
  `/app` must match both configured App ID and slug before any repository writes;
  status bot attribution derives from that verified identity. Its inventory
  must contain that one private repository. Status proof requires the configured
  context and the product App's bot login. The runner revokes this token.
- A separate file-referenced QA driver token supplies `contents:write` and
  `pull_requests:write` on that disposable repository only. Use a fine-grained
  token for `GITHUB_QA_DRIVER_LOGIN`. This driver causes actual push/PR events;
  it does not authorize the product or replace the App installation. The product
  manifest keeps its existing `contents:read` permission.

The runner adds a unique marker commit on the pinned QA branch, requires the
product App's real pending/success statuses on that exact SHA, and checks the
canonical active deployment's commit/repository/service/environment and
`provider_automation` trigger. It creates a unique QA head branch and draft pull
request, then waits for a canonical active preview deployment for its PR/SHA.
Cleanup closes the PR and waits for preview collection absence plus retirement
of the exact observed preview environment, removes its head
branch, deletes only the marker with matching content/blob identity, and waits
for the cleanup commit to deploy. It leaves the base branch in a working state
and intentionally retains the QA base service running the cleanup commit, plus
commits, closed PR, deployment/operation history, and audit rows. The cleanup
receipt names that retained runtime explicitly; it does not claim to destroy
the entire pre-existing QA service.
The driver token is pre-existing and is not revoked by the runner. A missing
create response followed by an empty lookup cannot certify cleanup; unresolved
PR/ref/file writes produce `CLEANUP_UNVERIFIED` and a `0600`
`acceptance-github-<run-id>.json` beside the private configuration. This journal
contains scoped resource identities and attempt flags, never credentials.

This is real GitHub delivery/build/status/preview automation only when executed
against those external systems. It does **not** drive interactive App creation,
OAuth installation, or workspace grant consent. Those identity handshakes remain
unverified by this lane. Preview activation readback does not prove public DNS,
certificate issuance, or externally reachable application HTTP. After an
environment is retired, human deployment reads return 404 as part of their
visibility filter. Those 404s do not independently prove physical container
removal; that limit remains explicit in the receipt unless the following
independent observer is configured. See
[GitHub App permission requirements](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps).

For physical preview cleanup proof, optionally configure `QA_RUNTIME_SSH_TARGET`,
`QA_RUNTIME_SSH_KEY_FILE`, and `QA_RUNTIME_SSH_KNOWN_HOSTS_FILE`;
`QA_RUNTIME_SSH_PORT` defaults to `22`. The private identity and pinned known-hosts
files use the same directory/file checks as other credentials. Optional
`QA_RUNTIME_SSH_SUDO=true` requires noninteractive permission for the observer's
read-only Docker commands. Preflight validates this configuration without
connecting. Execution runs static, bounded inspection commands through SSH; it
does not create, stop, delete, or otherwise mutate remote resources.

While the exact preview is canonically active, the observer must find a running
container with that deployment/project/service/environment label tuple. Cleanup
must then query the same Docker engine identity and find zero matching
containers, including stopped ones. The engine fingerprint and raw container
IDs remain in memory; receipts contain only fixed proof codes. A configured
observer that cannot establish presence, changes engines, or sees any residual
container fails the lane. Without the observer, the API/preview retirement lane
can pass while physical runtime removal remains explicitly unverified.

## Cloudflare DNS and native ACME staging

Use `--lane dns-acme` with a token restricted to the exact active Cloudflare zone
(`Zone:Read` and `DNS:Edit`), a dedicated child `DNS_QA_SUFFIX` beneath that zone,
and an **existing** native-issuer Let's Encrypt staging account. The private
account directory must contain its EC `account.key` and valid `registration.json`
for `acme-staging-v02.api.letsencrypt.org`. The runner refuses a production
account and creates no external account. It does not take over zone-apex, app
address, delegation, or existing TXT records.

Execution requests a unique `qa-<run-id>.<qa-suffix>` certificate from the staging
directory through `internal/acme.LegoIssuer`. A challenge wrapper uses the real
Cloudflare HTTP adapter and permits only that leaf's exact TXT challenge; a
CNAME rewrite outside the pinned leaf is refused. It journals the name/content
digest before creation, reads the provider record back, and deletes only a
matching owned record. The journal contains no TXT challenge value. Parent-side
cleanup also handles a lost creation response or killed issuer child. A missing
provider ID and repeated record absence remain ambiguous: cleanup waits briefly
for positive observation, deletes only the matching observed ID, and records the
deletion acknowledgement. If it never observes that write outcome, cleanup stays
unverified even after its bounded poll expires.

Lego's current native issuer does not propagate context through DNS propagation,
so the issuer runs in a killable child with vendor output suppressed. The parent
deadline bounds the entire issuance and separately verifies record absence.
Successful proof requires a returned staging certificate for the exact SAN with
current validity, then verified TXT cleanup and deletion of temporary copied
account/certificate private material. The original staging account remains.
On cleanup failure, a `0600` `acceptance-dns-<run-id>.json` beside the private
configuration retains only zone/name, content digest, record IDs, and deletion
acknowledgements. Copied keys, registration, certificate material, and child
configuration are erased. If the config-side journal cannot be saved, only its
sanitized source journal remains in the private temporary directory named with
the run ID; the receipt identifies that fallback without printing the path.
Use the private journal and scope configuration to review residual QA records;
do not publish them as public proof.

This directly exercises native staging issuance and the DNS adapter. It does
**not** prove the installed domain worker's reconciliation/ownership persistence,
Traefik certificate installation, public application HTTPS, production trust, or
renewal. Staging certificates are unsuitable for normal trusted browser HTTPS.
See [Cloudflare DNS record creation](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/create/),
[zone identity](https://developers.cloudflare.com/api/resources/zones/methods/get/),
and [Let's Encrypt staging](https://letsencrypt.org/docs/staging-environment/).

## Off-host recovery and installed TLS follow-through

The default `scripts/parity-restore-acceptance.sh` owns its disposable database
and temporary filesystem. It checks destruction/restoration, protected secret
fixture bytes and modes, tamper refusal, and replay. It accepts an explicit
credential-free `MIDNIGHT_PARITY_BACKUP_DESTINATION=s3://<bucket>/<qa-prefix>`
and the storage adapter's scoped credentials, but even an S3 run on that same
machine does not establish survival after loss of the original host.

Completing that drill needs an isolated replacement Linux VM with Docker and
compatible PostgreSQL tools, a dedicated off-host bucket/prefix and access to
its credentials, the backup passphrase held outside the original host, service
volume backup IDs, protected audit signing state, and authority to move only
the disposable QA hostname. Follow the [backup/restore runbook](runbooks/backup-restore.md)
to restore database, filesystem roots, and service volumes while writers are
offline. Keep the original QA host unavailable during readback, then verify
application bytes, login, deployment, audit signatures against independently
pinned keys, and public HTTPS on the replacement. No production destruction or
DNS move is implied by the local fixture script.

Installed certificate lifecycle proof additionally needs a delegated disposable
QA domain, the configured DNS provider, the native domain worker and ingress,
and authorization for staging issuance/install and renewal exercises. The
`dns-acme` lane above proves only its explicitly described adapter/issuer
boundary. Reconcile a real managed domain through the installed product and
retain independent DNS, route, certificate, HTTPS, and renewal observations
before calling that broader lifecycle verified.

## Local harness verification

`go test ./scripts/acceptance` covers strict configuration, credential file and
directory ownership/modes, symlink and writable-ancestor refusal, immutable child
configuration snapshots,
read-only/nonzero preflight, sanitized errors and redirects, exact correlated tool
proof, the conversation handler response shape and canonical router's real
administrator/nonadministrator/anonymous admission, mutation refusal and
versioned cleanup, lost creation responses, DNS leaf scope, lost DNS write
acknowledgement and delayed commits, unknown GitHub write outcomes, private
residual journals with copied-secret removal, changed-ownership refusal,
cancellation during cleanup, and bounded waits. Optional runtime observer
contracts cover positive presence, same-engine absence, engine changes, and
remaining stopped containers. HTTP/DNS/SSH doubles
are explicitly named in the tests. No test invents an external success receipt
or silently skips an absent external credential. Unix permission cases run on
Linux; the command blocks Windows execution instead of accepting weaker modes.
