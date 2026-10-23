# CLI flags: the canonical set

The Midnight CLI names one concept exactly one way. Historic spellings still
parse — nothing that worked before stops working — but they are hidden from
`--help` so there is only one thing to learn.

Every leaf command that takes flags lists them in `midnight <command> --help`.
If a flag is not in the help output, the command does not accept it.

## Framework flags

These are accepted by every command, in any position, before or after the
command's own flags and arguments.

| Flag | Meaning |
| --- | --- |
| `-o`, `--output table\|json\|jsonl\|name` | Output format. `table` is the human default; `json` is the server document verbatim; `jsonl` is one compact JSON value per line; `name` prints only the primary identifier, one per line, which is what you pipe into `xargs`. |
| `-q`, `--quiet` | Print only the primary result. Equivalent to `--output name`. |
| `--no-input` | Never prompt. A command that would have prompted fails with exit 64 and says which flag to pass instead. |
| `-y`, `--yes` | Approve confirmation prompts, including delete confirmations. |
| `--timeout <duration>` | Maximum command duration, for example `30s`. `0` uses the command default. |
| `--debug` | Diagnostic output. |
| `--no-color` | Strip ANSI colour from stdout. |

Exit codes: `0` success, `64` usage or local-configuration error (bad flags,
not linked, no server configured, `--no-input` with a missing answer), `1`
everything else.

## Canonical spellings

| Concept | Canonical | Also accepted (hidden) |
| --- | --- | --- |
| Project | `--project-id` | `--project` |
| Environment | `--environment-id` | `--environment`, `--env` |
| Service | `--service-id` | `--service` |
| API endpoint | `--api-base` | `--endpoint` |
| Row / entry cap | `--limit` | `--n`, `--tail` |
| Stream output | `--follow` | `--f` |
| Free-text filter | `--search` | `--q` |
| Deployment | `--deployment-id` | `--deployment` |
| Operation | `--operation-id` | `--operation` |
| JSON output | `--output json` | `--json` |

Selector flags accept a slug or an id wherever the command resolves names
(`midnight init`, `midnight link`, `midnight up`); elsewhere they take an id.

`--idempotency-key` is the one spelling for a caller-supplied replay key on
every mutating command. Omitting it is normal: commands derive a deterministic
key from the exact request intent.

## Reading the deploy state

```
midnight status                 # linked context, serving release, in-flight rollout, last failure
midnight status --output json   # the same as a document
```

`midnight status` reports:

- the linked project, environment, and service,
- `serving` — the deployment currently active in that environment,
- `in flight` — a queued or running rollout, if any,
- `last failure` — the most recent failed or canceled rollout, with the
  operation's failure code and reason.

## Getting started in a directory

```
midnight init                   # interactive: pick or create project, environment, service; writes the link
midnight init --no-input --project-id my-app --service-name web
midnight up
```

`midnight init` is idempotent. Re-running it in a linked directory reuses the
project, environment, and service that already exist and rewrites the link
file. Under `--no-input` it never prompts; pass `--name` (project slug to
create), `--project-id` (existing project), `--environment-id`,
`--environment-name`, `--service-id`, or `--service-name` as needed. It ends by
printing the next command to run.

## Deleting things

```
midnight service delete <service-id>
midnight project delete --yes <project-id>
midnight environment delete --yes <environment-id>
```

Delete verbs share one confirmation contract:

- `--yes` (or the framework `-y`) approves without prompting.
- `--no-input` without `--yes` is a usage error (exit 64), never a silent
  delete.
- Otherwise the command prints what will be destroyed and requires the exact
  slug or id to be retyped.

A server that has not shipped a delete route yet answers `405`/`501`; the CLI
reports that as "this server does not implement … deletion yet" rather than a
bare status line.

## Config as code and changesets

`midnight config validate|diff|apply|sync` and
`midnight changeset get|validate|apply|discard` take the canonical
`--api-base`, `--token`, `--idempotency-key`, `--file`, `--revision`, and
`--environment-id` flags, and list them in `--help` like every other command.
`--endpoint` continues to work as the hidden alias for `--api-base`.
