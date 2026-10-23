# Community Templates

Midnight can inspect a public community template at one immutable GitHub commit,
preserve its provenance, and create a project-owned editable fork. Community
templates never become official by copying a built-in slug or adding an
`official` tag. Built-in PostgreSQL, Redis, MySQL, and MongoDB templates keep
their separate verified provisioning path.

Discovery and forking are an authoring boundary. A local fork creates no
resources by itself. A valid fork can now be compiled into one durable
environment changeset for review; applying that draft updates runtime config,
encrypted variables, and volume intent atomically. Service creation, pinned
image deployment, and workload activation remain separate explicit actions.

## Source contract

Discovery accepts only:

- a public `https://github.com/{owner}/{repository}` source;
- an exact lower-case 40-character commit SHA; and
- `template.yaml` at the repository root.

Midnight fetches `template.yaml`, the declared README, and the declared SBOM
from `raw.githubusercontent.com`. It sends no GitHub token, disables redirects,
pins outbound connections to that host, rejects non-public resolved addresses,
and caps all response bodies. Branches, tags, arbitrary URLs, URL credentials,
query strings, and fragments are not source authority.

## `template.yaml`

The YAML decoder rejects unknown fields and multiple documents. A minimal
manifest has this shape:

```yaml
schema_version: 1
template:
  slug: queue-worker
  name: Queue worker
  version: 1.2.3
  summary: A pinned queue worker.
  tags: [worker]
  services:
    - slug: worker
      image: ghcr.io/acme/worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      env:
        LOG_LEVEL:
          kind: literal
          value: info
        TOKEN:
          kind: generated
          generated_kind: password
          length: 32
          secret: true
      ports:
        - port: 8080
          protocol: tcp
metadata:
  maintainer: Acme Platform <platform@example.com>
  license: Apache-2.0
  security_review: unreviewed
  risk_class: moderate
  update_policy: Pinned releases are reviewed monthly.
  deprecation_policy: Six months notice in the README.
  readme_path: README.md
  sbom:
    path: sbom.cdx.json
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
```

Replace the example SBOM digest with the lower-case SHA-256 of the exact SBOM
file committed at the selected ref.

Validation requires digest-pinned images, unique DNS-style service slugs, safe
repository-relative asset paths, bounded arguments and environment values,
valid ports and volume declarations, and resolvable computed environment
references. Literal values marked secret are rejected; generated passwords
must be sealed and 16–128 characters. Community manifests cannot reuse a
built-in template slug or claim the `official` tag.

## Discover, fork, and edit

Use **New project → Template** in the console to paste the repository and
commit SHA. Review the server-generated provenance, trust badge, maintainer,
license, security status, risk class, services, README, SBOM digest, and all
validation findings before creating a project.

The REST equivalent starts with read-only discovery:

```http
POST /api/v1alpha/templates/community/discover
Content-Type: application/json

{
  "source": "https://github.com/acme/queue-template",
  "ref": "0123456789abcdef0123456789abcdef01234567"
}
```

The response assigns `trust_class: community` and returns a
`provenance.manifest_sha256`. Fork creation re-fetches the same immutable
source and requires that digest, closing the review-to-create gap:

```http
POST /api/v1alpha/templates/community/forks
Idempotency-Key: community-queue-prj-demo
Content-Type: application/json

{
  "project_id": "prj_demo",
  "source": "https://github.com/acme/queue-template",
  "ref": "0123456789abcdef0123456789abcdef01234567",
  "expected_manifest_sha256": "<digest returned by discovery>"
}
```

The stored fork has `trust_class: local`. Its upstream manifest, commit, and
asset digests are immutable. Only `draft` changes. Reads return a strong ETag;
edits must send that version:

```http
PATCH /api/v1alpha/templates/community/forks/tpl_0123456789abcdef0123456789abcdef
If-Match: "1"
Content-Type: application/json

{"project_id":"prj_demo","draft":{...}}
```

Midnight saves invalid intermediate drafts with structured findings so an
editor does not lose work. `valid: false` is an explicit apply prohibition,
not a warning that can be overridden. A stale `If-Match` returns `412`; fetch
the current fork, reconcile the edit, and retry with its new ETag.

List the project's saved drafts with
`GET /api/v1alpha/templates/community/forks?project_id=prj_demo`. Full request
and response schemas are in the installation's `/api/v1alpha/openapi.yaml`.

## Stage a valid fork

In **Project settings → Community template drafts**, select an environment,
open a valid fork, and map every template service slug to one distinct existing
service. **Stage in changeset** compiles the fork on the server. Generated
values are created only at this boundary and are immediately handed to the
encrypted changeset authority.

The REST operation uses both fork and changeset compare-and-swap versions:

```http
POST /api/v1alpha/templates/community/forks/tpl_0123456789abcdef0123456789abcdef/changeset
If-Match: "0"
Idempotency-Key: stage-community-queue-prj-demo
Content-Type: application/json

{
  "project_id": "prj_demo",
  "environment_id": "env_production",
  "expected_revision": 0,
  "expected_fork_version": 2,
  "service_ids": {"worker": "svc_worker"}
}
```

The result is a redaction-safe draft. Apply it through the standard changeset
review surface. This operation does not create a service, deploy the fork's
digest-pinned image, start a workload, or upgrade the fork's `local` trust
class.
