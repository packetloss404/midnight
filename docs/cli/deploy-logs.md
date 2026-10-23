# Deploy Logs CLI

`midnight deploy events --operation-id <id>` streams backed operation lifecycle events from `GET /api/v1alpha/operations/{id}/events`.

`midnight deploy logs --operation-id <id>` reads `GET /api/v1alpha/operations/{id}/logs`. The deploy log line is the backed operation-event stream, and the build, runtime, HTTP, and platform sections only print real entries returned by a configured structured log store. When no store is configured, surfaces are marked unavailable with `LOG_STORE_UNAVAILABLE`; the command does not synthesize app logs from operation events.

Operators can narrow backed store reads with:

```sh
midnight deploy logs --operation-id op_123 --limit 100 --q "ready"
midnight deploy logs --operation-id op_123 --surface runtime --cursor lc1.OPAQUE_TOKEN
```

`--limit` applies per surface. `--q` matches message text or string fields.
`--cursor` accepts only the encrypted, authenticated token printed for a
truncated surface; host filenames and storage offsets are never exposed.
`--surface` is required and the token is also bound to the operation and
original text query, so it cannot be replayed across scopes.
