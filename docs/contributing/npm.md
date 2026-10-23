# npm Policy

Phase 0 uses npm with `package-lock.json` v3. Do not use pnpm or yarn for v0 unless `agile/handoff/PHASE0_DECISION_LOG.md` is updated and an ADR accepts the change.

Rules:

- Commit `web-next/package-lock.json` with every dependency change.
- Run `npm audit --omit=dev --prefix web-next` locally or through the manual
  supply-chain workflow before release evidence is accepted.
- Do not merge dependency changes without lockfile integrity hashes.
