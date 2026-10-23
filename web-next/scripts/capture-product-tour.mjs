import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const output = resolve(
  process.env.MIDNIGHT_CAPTURE_DIR || join(webRoot, "..", "artifacts", "console-tour"),
);

const playwrightCli = join(webRoot, "node_modules", "@playwright", "test", "cli.js");
const run = spawnSync(
  process.execPath,
  [playwrightCli, "test", "e2e/product-tour.e2e.ts", "--project=chromium", "--workers=1"],
  {
    cwd: webRoot,
    env: { ...process.env, MIDNIGHT_CAPTURE_DIR: output },
    stdio: "inherit",
  },
);

if (run.error) throw run.error;
if (run.status !== 0) process.exit(run.status ?? 1);
console.log(`Product tour assets written to ${output}`);
