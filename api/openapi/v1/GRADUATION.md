# Stable v1 graduation policy

Midnight treats `/api/v1alpha` as the design and compatibility surface and
`/api/v1` as a deliberately promoted stable contract. Copying or renaming the
alpha document does not constitute promotion.

## Machine gate

`go run ./tools/api-v1-graduation --format=json` always emits an inventory and
returns success when it can inspect the contract. This makes the report useful
while blockers remain. Add `--require-ready` for the release gate; it exits 3
when any blocker remains and 2 when inputs cannot be evaluated.

The gate evaluates these stable identifiers:

| Check | Promotion requirement |
| --- | --- |
| `no-501-responses` | No promoted operation advertises a `501` response. Unsupported work stays out of v1 until it exists. |
| `operation-tags` | Every operation has at least one audience or resource tag. |
| `mutation-examples` | Every schema-bearing mutating request body and successful response media type has a representation-level `example` or `examples`, either on the media type or resolved schema. |
| `conditional-mutations` | Every `PUT`, `PATCH`, and `DELETE` requires `If-Match`, documents `412` and `428`, and has a same-resource read returning a strong `ETag`. Body `expected_version` fields remain alpha compatibility debt and do not satisfy this rule. |
| `compatibility-error-envelope` | The stable projection is top-level RFC 9457 problem details only. Alpha may retain the nested `error` member only when it is both `deprecated: true` and `x-stable-v1-omit: true`; the stable generator removes that property and its `required` entry. |
| `problem-type-registry` | Root `x-problem-types` and the closed `ErrorResponse.code` enum exactly match the runtime HTTP problem registry. Every entry publishes a canonical HTTPS URI, stable title, recommended `status`, and a sorted closed `statuses` set containing that status. `.local`, localhost, relative, undocumented, and runtime-divergent entries are forbidden. |
| `bounded-json-boundary` | Handler source has no direct `json.NewDecoder(request.Body)` or `io.ReadAll(request.Body)` bypass; JSON requests use the shared size-bounded, unknown-field rejecting, duplicate/trailing-value rejecting exact-one-document decoder. |
| `generated-view-freshness` | Product, operator, agent, and UI views exactly match the canonical contract and audience manifest. |

The report schema is `midnight.api.v1-graduation.v1`. Check and finding order is
deterministic. Consumers should key automation on `schema_version`, `ready`,
`blocker_count`, and check `id`, not on human-readable messages.

The report distinguishes `canonical_operation_count` from the promoted
`operation_count` and reports `excluded_operation_count`. An operation is
excluded only when it declares `x-stability: alpha` and a non-empty
`x-stability-reason`. The generated stable bundle removes those operations
(and path items left empty), while the complete served alpha contract remains
unchanged. For the current candidate this is 182 canonical operations, 167
promoted operations, and 15 explicit exclusions. Passing the gate does not
claim stable support for an omitted operation.

## Promotion procedure

1. Make the strict gate pass against the canonical v1alpha bundle without
   suppressing findings.
2. Run router-to-contract and contract-to-router conformance, response-schema
   validation, generated Go and TypeScript client compilation, and the
   repository release checks.
3. Produce the stable projection in this directory. Change its server base to
   `/api/v1` and its API version metadata to `v1`, remove explicitly alpha
   operations, and remove only schema fields marked `x-stable-v1-omit`; do not
   mutate the v1alpha artifact in place.

   ```sh
   go run ./tools/api-contract-views \
     --stable-output api/openapi/v1/midnight.yaml
   ```
4. Re-run the strict graduation gate against the exact source revision being
   promoted and retain its JSON report as release evidence.
5. Review the stable diff for security-boundary changes and breaking wire
   changes. Tag and publish only after the diff and evidence are approved.
6. Keep v1alpha served during the announced migration period. Promotion does
   not silently redirect alpha clients to v1.

Passing this gate is necessary, not sufficient: it captures mechanical
graduation blockers while the conformance and generated-client checks prove
that the implementation honors the promoted document.

## Versioning rules

- The path major is the compatibility boundary. Backward-compatible additions
  may ship in `/api/v1`; breaking request, response, authentication, error,
  pagination, idempotency, or long-running-operation changes require a new
  major path.
- New response fields must be optional for existing clients. Existing required
  fields, meanings, defaults, status codes, and enum values cannot be removed
  or repurposed in v1.
- Adding an enum value is treated as potentially breaking unless the schema and
  published client guidance explicitly require unknown-value handling.
- Cursor payloads remain opaque. Servers may rotate their encoding and signing
  keys, but cannot change the documented ordering, scope binding, or expiry
  behavior within v1.
- Error `code` values and long-running operation terminal semantics are stable
  API surface, not implementation details.
- Replace, update, and delete concurrency uses RFC 9110 validators: strong
  response `ETag`, mandatory `If-Match`, `412 Precondition Failed` for a
  mismatch, and `428 Precondition Required` when the header is absent. Alpha
  body `expected_version` and `409` CAS behavior remain compatibility debt and
  are not promoted as the v1 concurrency contract.
- Problem `type` identifiers resolve through a public canonical documentation
  origin or use `about:blank`. Per-type title and recommended status are stable
  metadata. Established alpha codes that legitimately span statuses list every
  permitted value in a sorted closed `statuses` set; runtime emission outside
  that set fails closed. Development-only `.local` identifiers never appear in
  v1 responses.
- Generated audience views are projections, not independent contracts. Their
  source revision must match the canonical bundle.

## Deprecation and sunset rules

- A v1 element is deprecated in the OpenAPI document before removal, with a
  replacement and migration guidance in its description.
- Runtime responses for deprecated operations emit the standard `Deprecation`
  header. A scheduled removal also emits `Sunset` and a `Link` relation to the
  migration documentation.
- Announce a sunset at least 180 days before removal and support the deprecated
  behavior for at least two published minor release cycles, whichever is
  longer. Security fixes may shorten this only with a published incident and
  migration notice.
- Deprecation alone never authorizes removal from `/api/v1`. Removal waits for
  the next major API unless the behavior was explicitly documented as
  experimental and excluded from the stable contract.
- The v1alpha nested `error` envelope is a migration aid only. Stable v1 exposes
  the top-level RFC 9457 representation and does not carry both shapes.
