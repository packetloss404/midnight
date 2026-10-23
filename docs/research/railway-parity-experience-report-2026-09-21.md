# Railway-parity experience implementation report — 2026-09-21

## Verdict

The proof-led product experience is implemented and reviewable. Midnight now
shows the real console above the fold, includes a reproducible read-only tour,
publishes a candid capability comparison, and links claims to bounded evidence.

This work does **not** establish Railway usability parity. The last independent
score remains 7.7/10. Gate 9F still requires twelve independent human
participants and an installed reviewed journey before a parity claim can pass.

## Requested outcomes

| # | Outcome | Status | Evidence and boundary |
| ---: | --- | --- | --- |
| 1 | Real console recording | Implemented | `web-next/e2e/product-tour.e2e.ts` drives real routes and components and produces an eight-step WebM. The fixture uses synthetic IDs and reserved `.test` domains and refuses mutations. |
| 2 | Console proof above the fold | Implemented | The marketing hero embeds the generated WebM with a real resource-grid poster and an explicit synthetic-data disclosure. Full changes, topology, and logs captures appear in the tour. |
| 3 | Candid Railway comparison | Implemented | The site labels each capability Available, Operator configured, or Planned. It identifies Railway's hosted edge and infrastructure advantages and does not claim feature or usability parity. |
| 4 | Read-only interactive tour | Implemented | Keyboard-operable Workspace, Deployments, and Observe tabs explain the product model. The capture fixture independently returns HTTP 405 for mutations. |
| 5 | Install, HTTPS, backup, and GitHub evidence | Partially established | The dated VPS report proves the installed package/commit, schema 71, six active units, doctor checks, and registry/BuildKit listeners. Public application HTTPS, off-host restore, private-GitHub acceptance, and a candidate workload deployment remain open and are presented as open. |
| 6 | Independent usability test | Mechanism implemented; people still required | The study kit, JSON Schema, and `tools/gate9f` scorer enforce participant mix, completion, serving-release identification, apply/deploy prediction, confidence, SUS, accessibility, secret safety, tenant isolation, and truthful runtime state. They cannot substitute for independent participants. |

## Reproducibility

Run the real-console journey from `web-next`:

```sh
npm run capture:product-tour
```

Generated artifacts are written to the ignored
`artifacts/console-tour` directory. The marketing repository retains the
selected public assets under `public/product-tour` with visible provenance.

Score a completed human study from the repository root:

```sh
go run ./tools/gate9f -input /path/to/pseudonymous-results.json > gate9f-report.md
```

The scorer exits successfully only when every Gate 9F measure and safety
condition passes.

## Review and validation

The first independent peer review returned no-go and identified eight issues:
the real assets were not embedded, preview environments were mislabeled, three
Gate 9F safety checks were absent, an evidence anchor was broken, tour text and
mobile disclosure were weak, the comparison scroller lacked keyboard access,
runbook copy overstated retained evidence, and tests were too shallow.

The implementation corrected each issue. Local validation passed:

- control-plane lint and `tools/gate9f` tests;
- console typecheck and production build;
- the focused Chromium product-tour journey, including mutation refusal;
- marketing lint, production build, and 21 rendered-page and asset tests;
- desktop visual and accessibility-tree inspection of the rendered marketing
  page.

A second independent peer review returned **GO** with no release blockers. It
also verified the mobile layout at 375×812, embedded-video loading, mouse and
arrow-key tour navigation, the visible mobile disclosure, and the absence of
page-level horizontal overflow.

The complete application suite still has the unrelated timing-sensitive
failures recorded by the capture worker in `AdministratorPasswordReset` and
`TenantVariablesEditor`; the new product-tour journey, typecheck, and build pass
independently.
