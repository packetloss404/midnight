import { spawn, execFile } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { mkdir, writeFile, chmod, rm } from "node:fs/promises";
import { dirname, relative, resolve, isAbsolute } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { once } from "node:events";

const exec = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const runsRoot = resolve(root, ".midnight/browser-e2e");
const statePath = resolve(process.env.MIDNIGHT_PG_E2E_STATE_PATH || "");
const stateRelative = relative(runsRoot, statePath);
const runDirectory = dirname(statePath);
if (!stateRelative || stateRelative.startsWith("..") || isAbsolute(stateRelative) ||
    dirname(runDirectory) !== runsRoot || resolve(runDirectory, "state.json") !== statePath) {
  throw new Error("Browser state must be inside .midnight/browser-e2e");
}
if (process.platform === "win32") {
  throw new Error("Postgres browser acceptance needs Linux/macOS production KEK file modes; run this lane in Linux CI or a Linux shell.");
}

let ownedContainer;
let ownsRunDirectory = false;
let server;
let stopping = false;
async function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  if (server?.pid && server.exitCode === null && server.signalCode === null) {
    const exited = once(server, "exit").catch(() => {});
    server.kill("SIGTERM");
    let timeout;
    await Promise.race([exited, new Promise((done) => { timeout = setTimeout(done, 5_000); })]);
    clearTimeout(timeout);
    if (server.exitCode === null && server.signalCode === null) {
      server.kill("SIGKILL");
      await exited;
    }
  }
  if (ownedContainer) {
    await exec("docker", ["rm", "-f", ownedContainer], { timeout: 8_000 }).catch(() => {});
  }
  if (ownsRunDirectory) await rm(runDirectory, { recursive: true, force: true });
  process.exit(code);
}
process.on("SIGTERM", () => void stop());
process.on("SIGINT", () => void stop());

try {
  await mkdir(runsRoot, { recursive: true, mode: 0o700 });
  await mkdir(runDirectory, { mode: 0o700 });
  ownsRunDirectory = true;
  let dsn = process.env.MIDNIGHT_PG_E2E_DSN?.trim();
  if (dsn) {
    const parsed = new URL(dsn);
    if (!["postgres:", "postgresql:"].includes(parsed.protocol) ||
        !["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname) ||
        parsed.pathname !== "/midnight_browser_e2e") {
      throw new Error("MIDNIGHT_PG_E2E_DSN must target a disposable loopback database named midnight_browser_e2e");
    }
    for (const [name, value] of parsed.searchParams) {
      if (parsed.searchParams.getAll(name).length !== 1 ||
          !["sslmode", "connect_timeout"].includes(name) ||
          (name === "sslmode" && value !== "disable") ||
          (name === "connect_timeout" && !/^(?:[1-9]|[1-5][0-9]|60)$/.test(value))) {
        throw new Error("Browser DSN query permits only sslmode=disable and connect_timeout=1..60; connection overrides are forbidden");
      }
    }
  } else {
    ownedContainer = `midnight-browser-${randomUUID()}`;
    const password = randomBytes(24).toString("hex");
    await exec("docker", ["run", "--rm", "--detach", "--name", ownedContainer,
      "--label", "midnight.browser-e2e=true", "--publish", "127.0.0.1::5432",
      "--env", "POSTGRES_DB=midnight_browser_e2e", "--env", "POSTGRES_USER=midnight_e2e",
      "--env", `POSTGRES_PASSWORD=${password}`, "postgres:16-alpine"], { timeout: 120_000 });
    const { stdout } = await exec("docker", ["port", ownedContainer, "5432/tcp"]);
    const port = stdout.trim().split(":").at(-1);
    dsn = `postgres://midnight_e2e:${password}@127.0.0.1:${port}/midnight_browser_e2e?sslmode=disable`;
    let ready = false;
    for (let attempt = 0; attempt < 60; attempt++) {
      ready = await exec("docker", ["exec", ownedContainer, "pg_isready", "-U", "midnight_e2e", "-d", "midnight_browser_e2e"])
        .then(() => true, () => false);
      if (ready) break;
      await new Promise((done) => setTimeout(done, 500));
    }
    if (!ready) throw new Error("Disposable Postgres did not become ready");
  }
  const environment = { ...process.env };
  for (const name of Object.keys(environment)) {
    if (name.startsWith("MIDNIGHT_") || name.startsWith("PG") || name === "ANTHROPIC_API_KEY" || name === "OPENAI_API_KEY") delete environment[name];
  }
  for (const [variable, filename, raw] of [
    ["MIDNIGHT_CURSOR_KEY_PATH", "cursor.key", false],
    ["MIDNIGHT_AUTOMATION_TOKEN_KEY_PATH", "automation.key", false],
    ["MIDNIGHT_INVITATION_KEY_PATH", "invitation.key", false],
    ["MIDNIGHT_VARIABLE_INTENT_KEY_PATH", "variable-intent.key", false],
    ["MIDNIGHT_KEK_PATH", "kek", true],
  ]) {
    const path = resolve(runDirectory, filename);
    const material = randomBytes(32);
    await writeFile(path, raw ? material : material.toString("hex"), { mode: 0o400 });
    await chmod(path, 0o400);
    environment[variable] = path;
  }
  const operatorToken = randomBytes(32).toString("base64url");
  await writeFile(statePath, JSON.stringify({ operatorToken }), { mode: 0o600 });
  Object.assign(environment, {
    MIDNIGHT_DB_DSN: dsn,
    MIDNIGHT_API_BEARER_TOKEN: operatorToken,
    MIDNIGHT_AGENT_AUTH_DEVELOPMENT: "1",
    MIDNIGHT_AGENT_HMAC_SECRET: randomBytes(32).toString("hex"),
    MIDNIGHT_APPS_DOMAIN: "apps.browser.test",
  });
  const executable = resolve(runDirectory, "midnight-server");
  server = spawn("go", ["build", "-p", "2", "-o", executable, "./cmd/midnight-server"], {
    cwd: root, env: environment, stdio: "inherit",
  });
  await new Promise((done, reject) => {
    server.once("error", reject);
    server.once("exit", (code) => code === 0 ? done() : reject(new Error(`Browser API build failed with exit code ${code}`)));
  });
  server = spawn(executable, ["serve", "--storage=postgres", "--listen", "127.0.0.1:4182"], {
    cwd: root, env: environment, stdio: "inherit",
  });
  server.on("error", (error) => { console.error(error); void stop(1); });
  server.on("exit", (code) => { if (!stopping) void stop(code || 1); });
} catch (error) {
  console.error(error instanceof Error ? error.message : error);
  await stop(1);
}
