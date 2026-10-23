# Console product-tour capture

Midnight can render a deterministic, read-only tour of the real console without a running control plane or private credentials. The browser fixture drives the same routes and components used by the product. Its records use synthetic IDs and reserved `.test` hostnames.

From `web-next`, run:

```sh
npm run capture:product-tour
```

The command writes to `artifacts/console-tour` at the repository root:

- `01-resource-grid.png`
- `02-topology-map.png`
- `03-changes-apply-deploy.png`
- `04-service-overview.png`
- `05-deployments.png`
- `06-rollback.png`
- `07-logs.png`
- `08-metrics.png`
- `midnight-console-tour.webm`

Set `MIDNIGHT_CAPTURE_DIR` to write elsewhere. The output directory is intentionally ignored by Git because these files are generated marketing and review artifacts. The source fixture and browser journey are versioned, so every image and recording can be reproduced from a checkout.

The capture is evidence of implemented UI behavior, not production activity. It uses canonical API response shapes, exercises the real console, never contacts a live Midnight installation, and refuses unexpected mutations. Run the same journey as an ordinary browser test with:

```sh
npx playwright test e2e/product-tour.e2e.ts --project=chromium
```
