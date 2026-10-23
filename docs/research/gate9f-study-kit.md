# Gate 9F independent usability study kit

This kit turns the Gate 9F protocol in
[`../railway-ux-9-of-10-target-2026-09-01.md`](../railway-ux-9-of-10-target-2026-09-01.md)
into a repeatable, auditable study. It prepares and scores the study; it does
not replace independent human participants.

## Fixed study boundary

Record the exact Midnight commit, installed package identity, browser, viewport,
assistive technology, and seeded-project revision before the first session.
Every participant must receive the same seeded project and the same ten tasks.
Use pseudonymous participant IDs in the score input. Store names, contact
details, consent records, recordings, and raw notes separately under the study
operator's access controls.

Recruit at least:

- four current Railway users;
- four technically capable users new to Midnight and Railway;
- two self-hosting or platform operators;
- two keyboard or assistive-technology users.

Do not count a project contributor, session moderator, or anyone who saw the
seed data or task answers as an independent participant.

## Seeded scenario

Prepare two environments with the same three-service project:

- `production`: an API and PostgreSQL service are healthy; a newer API rollout
  has failed while the previous release still serves traffic;
- `staging`: the same API has an unapplied variable edit and no failed rollout;
- both environments expose an explicit private address, while only production
  has a generated public route;
- deployment history and logs contain enough evidence to diagnose the failed
  rollout without exposing a secret.

Reset the seed after every participant. Verify tenant isolation and confirm
that no participant can see another participant's notes or credentials.
Retain moderator evidence for the three study-wide safety assertions: no
secret exposure, no cross-tenant access, and no desired state presented as
observed runtime. Set the corresponding `safety` fields to `true` only after
the moderator has checked every session; any `false` value forces a failed
gate even when the experience scores pass.

## Moderator script

Read this introduction verbatim:

> You are testing the product, not being tested yourself. Work as you normally
> would and say what you expect before each action. I will not point to controls
> or explain terminology. If you become blocked, tell me what you expected to
> find. You may stop at any time.

Ask the participant to complete the ten tasks from Gate 9F without product
documentation or hints. For each task record completion without help, duration,
wrong turns, and confidence from 1 to 7. Task 2 also records whether the
participant identified the release actually serving traffic. Tasks 5 and 6
record whether the participant correctly predicted Apply & Deploy versus
apply-only before acting.

After all tasks, collect the standard ten SUS answers on a 1–5 scale and the
four 1–10 ratings: Railway familiarity, new-user ease, UX coherence, and
overall experience. Record accessibility findings as `minor`, `serious`,
`critical`, or `blocking`; include the affected task and recovery behavior in
the private notes.

## Results format and scoring

Create JSON matching
[`gate9f-results.schema.json`](gate9f-results.schema.json). Use task IDs 1–10
in the order defined by the parent protocol. Tasks 1–9 are the core-task
completion denominator; task 10 is the keyboard-specific observation.

Generate the immutable Markdown score report with:

```sh
go run ./tools/gate9f -input /path/to/pseudonymous-results.json > gate9f-report.md
```

The command exits `0` only when every Gate 9F threshold passes, `1` when the
study is valid but misses any threshold, and `2` when the input is incomplete
or invalid. Commit only the pseudonymous score report and the exact product and
seed identities. Never commit recordings, consent records, participant contact
details, credentials, secrets, or raw free-form notes.

## Publication rule

Until an independent study passes, public copy may say that the workflow is
Railway-like and that the study is prepared or scheduled. It must not say that
Midnight matches Railway's usability, earns 9/10, or is proven easier. A failed
or partial study remains useful product evidence and must not be discarded or
silently rerun with easier tasks.
