# Developer Setup

Required tools for Phase 0 scaffold work:

- Go 1.25 or newer (`go.mod` pins the exact toolchain)
- Node.js 20 or newer
- npm 10 or newer
- Optional: Task

Run:

```sh
make bootstrap
make build
make test
```

Native Windows is not a supported runtime target for Midnight. Use Linux, macOS, or WSL for product development.

## DNS provider development

The Cloudflare DNS connector is not required for ordinary local development.
Unit and contract tests
must use an HTTP test double and must not require a Cloudflare account or
credential. The live lane needs a disposable Cloudflare zone, a narrowly
scoped API token, and an externally reachable VM; never commit or paste the
token into source, fixtures, logs, or chat. The design and installed-VM
sequence are in [`docs/networking/cloudflare-dns.md`](../networking/cloudflare-dns.md).
