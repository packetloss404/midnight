## Summary

- 

## Story / Panel Decision

- Story:
- Panel decision link or `no panel decision touched`:

## Architecture & Decision Trail

- [ ] If this PR changes architecture, an ADR was added under
      `docs/adr/NNNN-<slug>.md` and the index was regenerated; otherwise
      ticked because no ADR was needed.
- [ ] Panel-decision text in `midnight-platform-plan.md` is unchanged; any
      panel-derived behavior added in this PR cites the panel-decision
      verbatim in the linked ADR or sprint story.

## Error Codes

- [ ] No new error codes, OR each new code:
  - is added to `internal/failurecodes/codes.go` as a typed
    `failurecodes.Code` constant, AND
  - has a registry row in `docs/error-codes.md` with `Owner`, `Notes`, and
    `Recovery hint`, AND
  - leaves the registry sorted alphabetically (the
    `LINT_REGISTRY_GUARD_FAILED` lint check enforces this in local and manual
    lint runs).

## Drill Manifest

- [ ] Behavior change does not cross a row in `test/drills/manifest.yaml`,
      OR the matching drill row was updated and `make phase0-drills` still
      reports the expected pass count.

## Tests

- [ ] `make lint`
- [ ] `make test`
- [ ] `make supplychain`
- [ ] `make release-check` if the PR touches `tools/`, `Makefile`,
      `Taskfile.yml`, `.github/workflows/`, or release evidence emitters.

## Documentation Impact

- [ ] Docs/OpenAPI/runbooks/error codes updated or not applicable.

## Security Impact

- [ ] Security review required.
- [ ] No security impact.
