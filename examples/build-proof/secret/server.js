const http = require("node:http");
const fs = require("node:fs");

const proof = fs.readFileSync("secret-proof.txt", "utf8").trim();
http
  .createServer((_request, response) => {
    response.writeHead(200, { "content-type": "text/plain; charset=utf-8" });
    response.end(proof);
  })
  .listen(8080, "0.0.0.0");
