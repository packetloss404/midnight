"use strict";

const http = require("node:http");

const port = Number(process.env.PORT || "8080");
const message = process.env.GREETING || "midnight build proof";

const server = http.createServer((req, res) => {
  if (req.url === "/healthz") {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ status: "ok" }));
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end(`${message}\n`);
});

server.listen(port, () => {
  // The build-proof script greps stdout for this line to confirm startup
  // before issuing HTTP probes against the allocated route.
  console.log(`build-proof listening on :${port}`);
});

const shutdown = () => {
  server.close(() => process.exit(0));
};
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
