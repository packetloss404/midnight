const fs = require("node:fs");

if (!process.env.PRIVATE_TOKEN) {
  throw new Error("PRIVATE_TOKEN was not mounted as a build secret");
}

// Deliberately echo the value. Midnight's buildctl boundary must redact this
// before the progress stream reaches an operation event or durable log.
process.stdout.write(`adversarial-build-output=${process.env.PRIVATE_TOKEN}\n`);
fs.writeFileSync("secret-proof.txt", "build secret was available without being persisted\n");
