# Midnight documentation

This repository contains control-plane documentation for contributors and
operators. The public website and reader-facing documentation are maintained
separately in `packetloss404/midnight-marketing`.

## Start here

| Need | Maintained source |
| --- | --- |
| Understand the product | [Repository README](../README.md) |
| Resume development | [Handoff](../HANDOFF.md) |
| Choose unfinished work | [Backlog](../BACKLOG.md), the only ordered work queue |
| Assess recorded deployment evidence | [Deployment readiness](deployment-readiness.md) |
| Decide whether MVP acceptance passes | [Acceptance contract](mvp-acceptance.md) |
| Check advertised feature coverage and missing proof | [Feature verification ledger](feature-verification.md) |
| See changes over time | [Changelog](../CHANGELOG.md) |

## Build and operate

- [Local quickstart](quickstart.md), [development stack](dev-local-stack.md),
  and [contributor setup](contributing/dev-setup.md).
- [Operator installation](install/operator-install-path.md),
  [live deploy proof](runtime/live-deploy-proof.md), and
  [operator readiness](runbooks/operator-readiness.md).
- [Custom-domain cutover](runbooks/custom-domain-cutover.md) for moving an
  existing service from its generated hostname to an ownership-verified host.
- [CLI reference](../cmd/midnight/README.md), [flags](cli/flags.md),
  [config as code](cli/config-as-code.md), and [API documentation](api/README.md).
- [Build pipeline](build-pipeline.md), [GitHub App](github-app.md),
  [variables](variables-lifecycle.md), and [official database templates](templates/official-databases.md).
- [Durable HTTP routing](networking/durable-http-routing.md),
  [private networking](networking/private-networking.md), and
  [DNS reconciliation](networking/cloudflare-dns.md).
- [Backup/restore](runbooks/backup-restore.md), [TLS failures](runbooks/domain-tls-failure.md),
  [alert triage](runbooks/alert-triage.md), and [support bundles](runbooks/support-bundle.md).
- [Authentication](security/auth-foundations.md), [untrusted builds](security/untrusted-builds.md),
  and [security reporting](../SECURITY.md).

## Architecture, releases, and evidence

- [Repository layout](architecture/repo-layout.md) and [live ADR index](adr/README.md).
- [Release verification](release-verify.md), [release evidence](governance/release-evidence.md),
  [merge policy](governance/merge-policy.md), and [CLI distribution](release/cli-distribution.md).
- [Parity runner evidence protocol](railway-parity-runner-evidence.md) remains active.
- [Configuration UX acceptance protocol](railway-ux-9-of-10-target-2026-09-01.md)
  remains referenced by `MID-108`; its date does not make the outstanding Gate 9F obsolete.
- [Gate 9F independent study kit](research/gate9f-study-kit.md) fixes the
  participant, task, evidence, privacy, and machine-scoring procedure; it does
  not substitute for real independent participants.
- [September 24 installed VPS and public-domain verification](research/installed-vps-verification-2026-09-24.md)
  records the current package, schema, instance namespace, custom workload
  domain, and edge-terminated HTTPS evidence with explicit boundaries.
- [September 25 Cloudflare wildcard DNS and TLS verification](research/cloudflare-wildcard-tls-verification-2026-09-25.md)
  records authoritative delegation, generated-app wildcard DNS, edge-managed
  wildcard HTTPS, restart persistence, and the boundary around application
  route readiness.
- [September 21 installed VPS verification](research/installed-vps-verification-2026-09-21.md)
  is the preceding package observation and remains historical evidence.
- [September 21 Railway-parity experience report](research/railway-parity-experience-report-2026-09-21.md)
  records the proof-led marketing and console-tour implementation, peer-review
  corrections, validation, and the boundary around the still-required human
  study.
- [Archive index](archive/README.md) collects dated reviews, handoffs, and the
  earlier planning bundle. Historical findings do not reopen completed work.

## Keeping status consistent

`BACKLOG.md` owns work state; `HANDOFF.md` summarizes the source checkpoint;
`deployment-readiness.md` records dated installed evidence; `mvp-acceptance.md`
defines the required assertions. Update the relevant sources together when a
change alters those facts. Record commit, date, environment, and evidence scope;
do not turn a past host observation into an undated current-health claim.

Accepted ADRs and maintained API contracts describe intended behavior. The
original panel decisions remain the Phase 0 baseline under ADR-0001; later
accepted ADRs document changes. The two ADR collections are not one numbering
sequence, and the `agile/` planning index is not a live architecture index.

Before moving documentation, check incoming links, code comments, lint guards,
and executable scripts. Preserve references from immutable migrations with a
small redirect. Keep each new report in the archive once its work state has
been reconciled into the maintained sources.
