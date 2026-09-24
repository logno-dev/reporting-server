# Typst Report Service

Initial backend for durable, reproducible Typst report generation.

## Run locally

```sh
cp .env.example .env
docker compose up --build
```

## Application integration API

The machine API is intended for trusted internal applications such as a LIMS. Report generation is asynchronous: discover a published contract, submit data, poll the returned job, and download the completed PDF.

Use the report service's HTTPS origin as the base URL, for example `https://reports.example.com`. All request and response bodies are JSON unless an endpoint returns a PDF.

### Authentication and scopes

An administrator creates an API client and issues its key in the **API Clients** workspace. The plaintext key is shown only once. Send it as a bearer token:

```http
Authorization: Bearer rpt_live_<key-id>_<secret>
```

`X-API-Key` is accepted for compatibility, but bearer authentication is preferred. Never place a key in a URL, browser application, log, or source repository.

The examples below assume:

```sh
export REPORT_API_URL='https://reports.example.com'
export REPORT_API_KEY='rpt_live_...'
```

Scopes are independent. A client implementing the complete workflow normally needs all four:

| Scope | Permitted operation |
| --- | --- |
| `templates:read` | Discover published template versions and contracts |
| `reports:submit` | Submit report jobs |
| `reports:read` | Poll and search report jobs |
| `reports:download` | Download completed PDFs |

Keys authorize service-wide access for their scopes; report jobs are not isolated by API client. Issue keys only to trusted applications and grant the minimum required scopes.

### Recommended client workflow

1. Fetch `GET /v1/report-templates` during integration setup or on a controlled refresh schedule.
2. Select a concrete published `version` and retain its `schemaHash`.
3. Validate application data against that version's `dataSchema`.
4. Submit the exact `template`, `version`, `schemaHash`, and `data` to `POST /v1/reports`.
5. Persist the returned `jobId`; report submission itself is not idempotent.
6. Poll `GET /v1/reports/{jobId}` until the job is `completed` or `failed`.
7. Download a completed PDF from its `downloadUrl` and optionally verify `X-Content-SHA256`.

Pinning both `version` and `schemaHash` is recommended for production integrations. It prevents a newly published contract from silently changing an application's expected payload. Omitting `version` explicitly opts into the latest published version; the server still resolves and permanently pins a numeric version when it creates the job.

### Discover report contracts

`GET /v1/report-templates` requires `templates:read` and returns only immutable published versions from active templates. Typst source, sample data, drafts, archived templates, and editor metadata are not exposed. Archiving is a discoverability control rather than deletion: existing reports and explicitly version-pinned submissions continue to work.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $REPORT_API_KEY" \
  "$REPORT_API_URL/v1/report-templates"
```

Example `200 OK` response:

```json
{
  "items": [
    {
      "slug": "certificate-of-analysis",
      "name": "Certificate of Analysis",
      "latestVersion": 2,
      "versions": [
        {
          "version": 2,
          "publishedAt": "2026-09-23T21:54:00Z",
          "dataSchema": {
            "$schema": "https://json-schema.org/draft/2020-12/schema",
            "type": "object",
            "properties": {
              "sample": {
                "type": "object",
                "properties": {
                  "id": {"type": "string"},
                  "description": {"type": "string"}
                },
                "required": ["description", "id"],
                "additionalProperties": true
              },
              "results": {
                "type": "array",
                "items": {
                  "type": "object",
                  "properties": {
                    "result": {"type": "string"},
                    "test": {"type": "string"}
                  },
                  "required": ["result", "test"],
                  "additionalProperties": true
                }
              }
            },
            "required": ["results", "sample"],
            "additionalProperties": true
          },
          "schemaHash": "<schema-sha256>"
        }
      ]
    }
  ]
}
```

Treat `dataSchema` as the authoritative JSON Schema for that version. A schema hash is lowercase hexadecimal SHA-256 and changes when the generated contract changes. Versions published before contract analysis was introduced may expose a permissive object schema for compatibility.

### Submit a report

`POST /v1/reports` requires `reports:submit`.

```sh
curl --fail-with-body \
  -X POST "$REPORT_API_URL/v1/reports" \
  -H "Authorization: Bearer $REPORT_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "template": "certificate-of-analysis",
    "version": 2,
    "schemaHash": "<schemaHash returned by the catalog>",
    "data": {
      "sample": {
        "id": "26000123",
        "description": "Example sample"
      },
      "results": [
        {"test": "APC", "result": "<10"},
        {"test": "Salmonella", "result": "ND"}
      ]
    }
  }'
```

| Field | Required | Description |
| --- | --- | --- |
| `template` | Yes | Published template slug from the catalog |
| `version` | Recommended | Positive published version; omit to use latest published |
| `schemaHash` | Recommended | Exact hash from the selected catalog version |
| `data` | Yes | Report input that satisfies the selected `dataSchema`; maximum 2 MiB |

Unknown request fields are rejected. A successful submission returns `202 Accepted`:

```json
{
  "jobId": "01M384CQDRYVREG5FZN72VQWX3",
  "status": "queued",
  "template": "certificate-of-analysis",
  "templateVersion": 2,
  "schemaHash": "<resolved-schema-sha256>"
}
```

Contract resolution, validation, job creation, and durable queue intent creation occur in one PostgreSQL transaction. Redis downtime does not require client resubmission; the service dispatches the stored queue intent after Redis recovers.

Each successful submission creates a new report. `POST /v1/reports` does not currently support `Idempotency-Key`, so an application retry after an ambiguous network failure can create a duplicate. Persist the response promptly and reconcile uncertain submissions with `GET /v1/reports` before retrying when duplicates matter.

### Poll a report

`GET /v1/reports/{jobId}` requires `reports:read`.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $REPORT_API_KEY" \
  "$REPORT_API_URL/v1/reports/01M384CQDRYVREG5FZN72VQWX3"
```

Example completed response:

```json
{
  "jobId": "01M384CQDRYVREG5FZN72VQWX3",
  "template": "certificate-of-analysis",
  "templateVersion": 2,
  "status": "completed",
  "dataSha256": "90b0ef21e7f4042e9dab597313d570aab8fd89054f8d93047b78355bc8a23f6a",
  "requestedBy": "api-client:01M384CQDRYVREG5FZN72VQWX3:key:01M38CN9T4ZR01FFGHDQ1GJ20G",
  "createdAt": "2026-09-23T21:57:47.576758Z",
  "startedAt": "2026-09-23T21:57:49.236335Z",
  "completedAt": "2026-09-23T21:57:49.283798Z",
  "attempts": 1,
  "storageKey": "reports/2026/09/23/01M384CQDRYVREG5FZN72VQWX3.pdf",
  "storageProfile": "production-r2",
  "sha256": "8370ffacb1ca97239e4b5ebd56d66eac1d56a0fa2bc7fe7de6a1ef26a30fda9e",
  "rendererVersion": "typst-0.13.1",
  "downloadUrl": "/v1/reports/01M384CQDRYVREG5FZN72VQWX3/download",
  "schemaHash": "<resolved-schema-sha256>",
  "retryChildren": []
}
```

Possible states are:

| Status | Meaning | Client action |
| --- | --- | --- |
| `queued` | Waiting for a worker or an automatic retry | Continue polling |
| `processing` | Claimed by a worker | Continue polling |
| `completed` | PDF is available | Follow `downloadUrl` |
| `failed` | Retry budget was exhausted | Record `error` and contact an operator if appropriate |

A temporary render or storage failure may move a job from `processing` back to `queued`, so clients must not assume progress is monotonic. Poll with bounded exponential backoff, for example beginning at one second and stopping at a timeout appropriate for the application. The service does not currently return a `Retry-After` header.

The immutable input payload is deliberately omitted from job responses. Optional operational fields such as `error`, `processingWorkerId`, `processingLeaseUntil`, retry lineage, output metadata, and timestamps appear only when relevant.

### Search reports

`GET /v1/reports` requires `reports:read` and returns newest jobs first.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $REPORT_API_KEY" \
  "$REPORT_API_URL/v1/reports?status=completed&sampleId=26000123&limit=50"
```

| Query parameter | Description |
| --- | --- |
| `status` | Exact `queued`, `processing`, `completed`, or `failed` status |
| `template` | Exact template slug |
| `templateVersion` | Exact positive version |
| `sampleId` | Case-insensitive text search across the serialized report input; despite its name, it is not restricted to one JSON path |
| `requestedBy` | Exact verified caller identity, including client and key IDs for machine callers |
| `createdFrom` | Inclusive RFC3339 creation time |
| `createdTo` | Exclusive RFC3339 creation time |
| `limit` | Page size from 1 to 200; default 50 |
| `offset` | Zero-based offset; default 0 |

Example response:

```json
{
  "items": [],
  "limit": 50,
  "offset": 0,
  "hasMore": false
}
```

Use `offset + limit` while `hasMore` is true. Report records remain searchable in PostgreSQL if an application loses a previously returned job ID.

### Download a PDF

`GET /v1/reports/{jobId}/download` requires `reports:download`. `HEAD` is also supported when an application only needs to check availability or inspect metadata.

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $REPORT_API_KEY" \
  -o report.pdf \
  "$REPORT_API_URL/v1/reports/01M384CQDRYVREG5FZN72VQWX3/download"
```

A successful response is `200 OK` with these headers:

```http
Content-Type: application/pdf
Content-Disposition: attachment; filename="certificate-of-analysis-v2-01M384CQDRYVREG5FZN72VQWX3.pdf"
Content-Length: 17598
Cache-Control: private, no-store
ETag: "8370ffacb1ca97239e4b5ebd56d66eac1d56a0fa2bc7fe7de6a1ef26a30fda9e"
X-Content-SHA256: 8370ffacb1ca97239e4b5ebd56d66eac1d56a0fa2bc7fe7de6a1ef26a30fda9e
```

The API retrieves the object through the configured storage profile and verifies its recorded SHA-256 digest before returning it. Applications never need object-store credentials.

### Errors

API errors use a stable machine-readable code and a human-readable message:

```json
{
  "error": {
    "code": "data_validation_failed",
    "message": "report data failed template contract validation",
    "details": [
      {
        "path": "$.sample.id",
        "message": "required field is missing"
      }
    ]
  }
}
```

`details` is present for field-level validation failures. Applications should branch on the HTTP status and `error.code`, not the message text.

| Status | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_request` or `invalid_*` | Malformed JSON, unknown fields, or invalid query parameters |
| `401` | `authentication_required` or `invalid_api_key` | Missing or invalid credentials |
| `403` | `insufficient_scope` | The key lacks the required scope |
| `404` | `not_found` | Report or endpoint was not found |
| `409` | `template_contract_changed` | Submitted `schemaHash` no longer matches the selected version |
| `409` | `report_not_ready` | The requested PDF is not completed yet |
| `413` | `request_too_large` | Report `data` exceeds 2 MiB |
| `422` | `template_not_found` | Template or version is not published |
| `422` | `data_validation_failed` | Report data does not satisfy the selected contract |
| `500` | `internal_error` or `artifact_integrity_error` | Server failure or stored PDF integrity failure |
| `503` | `storage_unavailable` | The completed artifact cannot currently be read |

The maximum JSON request body is 10 MiB, with a stricter 2 MiB limit on report `data`. Retry `5xx` and network failures with backoff. Do not automatically retry contract, validation, authentication, or authorization errors without correcting the request or credentials.

`GET /healthz` is unauthenticated and is intended for deployment health checks, not application workflow decisions.

## Template Manager

Open `http://localhost:8080` to use the embedded template workbench. It includes:

- CodeMirror editing for Typst source and JSON test data
- On-demand PDF compilation and preview
- Template and version navigation
- Draft creation and saving
- Dirty-state warnings, keyboard save/preview shortcuts, and draft discard
- Draft approval and immutable publishing
- Debounced contract analysis that adds missing fake-data fields, displays diagnostics and a schema hash, and blocks unsafe approval

The lifecycle is `draft -> approved -> published`. **Save & approve** persists pending changes and advances a valid draft in one action; approval compiles and locks the version before publication. Published history opens read-only; use **Create editable draft** or **Open draft** to make changes. Approved versions are locked, and published versions cannot be modified by the application or directly in Postgres.

Administrators can archive and restore an entire template. Archived templates are hidden from the published machine catalog and the default editor list, but their immutable versions, existing reports, downloads, explicit version-pinned submissions, retry lineage, and storage placements remain functional.

Administrators also have **Reports**, **Storage**, and **API Clients** workspaces. Reports provides operator filters, immutable metadata and lineage, the append-only attempt timeline, PDF download, failed-report retry, worker/queue health, and guarded stale recovery. API clients own a validated set of machine scopes. Keys snapshot those scopes when issued, so changing a client does not silently expand an existing key; disabling a client invalidates all its keys immediately. Plaintext keys are shown only in the issue or rotation response.

## Report operations

The browser operations API is administrator-only and always obtains its audit actor from the verified OIDC session:

- `GET /v1/admin/reports/{id}` returns report metadata and its append-only attempt timeline without returning the report input.
- `POST /v1/admin/reports/{id}/retry` accepts only failed reports. It creates a new ULID and outbox row, copies the original immutable data and exact published template version, pins the storage profile that is the default at retry time, and links the new job through `retryParentId`. The failed row and any existing PDF are untouched. An optional `Idempotency-Key` of up to 200 characters is scoped to parent report and administrator identity; replay returns the same child.
- `GET /v1/admin/operations/health` reports worker heartbeat age/state, report status counts, oldest queued age, report outbox depth, pending/running storage migrations, and Asynq `default` and `storage-migration` queue counters. Queue inspection uses Asynq's supported Inspector API. If Redis inspection is temporarily unavailable, database health remains available with a non-sensitive `queueError`.
- `POST /v1/admin/reports/{id}/recover` performs explicit stale recovery. It succeeds only for a `processing` report whose processing lease has expired and whose owning worker has no heartbeat in the last 30 seconds. In one transaction it returns the report to `queued`, inserts or unlocks its outbox row, and appends a `recovered` event naming the administrator. Active workers and unexpired leases are rejected with `409`.

Workers identify themselves with a process ULID, heartbeat every 12 seconds, and publish start time, last seen time, concurrency, renderer version, and active report count. Each report claim sets a worker-owned lease longer than `RENDER_TIMEOUT`; started, queue-retry, terminal-failure, completion, and operator-recovery events are append-only. A deterministic Asynq task ID still prevents ordinary duplicate delivery. Recovery does not delete or force-replace an active Redis task: the transactional outbox attempts normal deterministic enqueue, and an existing Asynq task remains responsible for eventual redelivery. Consequently, recovery is safe but may wait for Asynq to reclaim a task left active by a crashed process.

Worker heartbeat rows are operational liveness records, not an audit log; each worker updates its own row and old rows remain visible as offline until cleaned administratively. Report attempt events and retry lineage are database-protected against updates/deletes.

## Authentik

Production browser access uses an existing Authentik instance through OpenID Connect. Authenticated users can view templates and compile previews. Members of the configured admin group can create, edit, approve, and publish templates.

In Authentik:

1. Create an OAuth2/OpenID Provider using the authorization-code flow.
2. Set the strict redirect URI to `https://reports.example.com/auth/callback` using the real report-manager domain.
3. Include the standard `openid`, `profile`, and `email` scope mappings and ensure the ID token contains the `groups` claim.
4. Create an application linked to the provider.
5. Create the `report-admins` group, or configure a different name with `OIDC_ADMIN_GROUP`.
6. Add template administrators to that group.

Configure the report service:

```env
APP_ENV=production
OIDC_ISSUER_URL=https://auth.example.com/application/o/reporting/
OIDC_CLIENT_ID=...
OIDC_CLIENT_SECRET=...
OIDC_REDIRECT_URL=https://reports.example.com/auth/callback
OIDC_ADMIN_GROUP=report-admins
SESSION_SECRET=a-random-secret-at-least-32-characters-long
```

Create machine clients and issue keys from the administrator-only **API Clients** workspace. Send a key as `Authorization: Bearer rpt_live_...`; `X-API-Key` remains accepted as a compatibility header. Supported scopes are `templates:read`, `reports:submit`, `reports:read`, and `reports:download`. Template management remains browser-only. Lifecycle audit identities come from verified OIDC sessions, and machine request identities come from the verified client and key rather than caller-supplied headers.

Test JSON is mounted into each render workspace as both `report.json` and `data.json`. Templates can load either name with `#let report = json("report.json")` or `#let data = json("data.json")`.

Contract analysis intentionally covers common data access: dotted paths, aliases, loop aliases, and literal `.at("field", default: ...)`. It ignores comments and string contents and blocks dynamic rooted lookup at approval. It is not complete static analysis of arbitrary Typst; templates using metaprogramming or other access forms may need to be rewritten into supported explicit accesses.

## Storage

Published template sources and generated PDFs use storage profiles managed from the administrator-only **Storage** workspace.

### Local persistent storage

The application always adopts `REPORTS_DIRECTORY` as its deployment-managed local profile. Compose maps this to the persistent `reports` volume, which survives container replacement and application upgrades. It remains the default until an administrator explicitly selects another profile.

### External S3-compatible storage

Create Cloudflare R2, AWS S3, MinIO, or another S3-compatible profile from **Storage** in the application. Bucket configuration and credentials are encrypted in PostgreSQL rather than configured through environment variables. The bucket must already exist.

The worker synchronizes immutable published template versions to `templates/{slug}/v{version}/main.typ` and reads them from configured storage when rendering. PDFs are written below `reports/{year}/{month}/{day}/`.

### Overlapping migration

Set `STORAGE_MASTER_KEY` to the same base64-encoded 32-byte key on every API and worker replica. It encrypts profile credentials at rest; changing or losing it makes stored profiles unusable. Generate one with `openssl rand -base64 32` and store it in the deployment secret manager, not in source control.

The migration workflow is deliberately separate from cutover:

1. In **Storage**, create an S3-compatible profile. The service validates all fields, writes and removes a random connectivity probe, and only then persists encrypted credentials. Credentials are never returned by the API or placed in Redis.
2. Create a migration from an existing profile to the new profile. Creation transactionally snapshots every currently available source placement, including its storage key, digest, and byte count. Objects written later require another migration.
3. Start the draft. The Postgres dispatcher sends ID-only tasks to the dedicated `storage-migration` Asynq queue. Pause stops new dispatches; in-flight copies may finish. Resume retries failed items. Cancel stops dispatch permanently. Sources are never deleted.
4. Review completed/skipped and failed counts. A copy reads the snapshotted source, verifies known SHA-256 and size, writes the same key, verifies the storage result, and atomically records the destination placement. Normal reads continue to fall back through all available placements.
5. Explicitly choose **Set default** when new reports should be written to the destination. Copy completion never changes the default.

Limits are global across replicas because dispatch is coordinated in Postgres. A per-profile transaction advisory lock counts unexpired `running` item leases against `transfer_concurrency`; the default is one. `request_rate_limit` spaces the start of destination transfers by `1/rate` seconds; when omitted there is no start-rate cap. A failed transient transfer is returned to durable scheduling with exponential backoff (up to 60 seconds and five attempts). Integrity/configuration failures are terminal and visible until resume. Worker tasks have deterministic IDs containing migration ID, object ID, and attempt, and Redis payloads contain only migration/object IDs. Each worker runs a dedicated migration consumer separately from the `default` report consumer, reserving the configured report concurrency entirely for rendering; migration transfers therefore never occupy report worker slots. Reports remain the operational priority, although both pools still share host CPU and network capacity.

Admin-only endpoints are under `/v1/storage`: `profiles`, `profiles/{id}/test`, `default`, `migrations`, `migrations/{id}`, and migration `pause`, `resume`, and `cancel` actions. The bootstrapped legacy filesystem profile is visible and selectable, but the API cannot create arbitrary filesystem roots.

Storage is portable, but it is not the only stateful component. Postgres contains template metadata, report records, input snapshots, and audit data and must remain available or be migrated with the deployment. Redis only contains queue execution state; drain the queue before moving or accept re-enqueuing unfinished jobs.

## Health checks

The Compose definition configures Docker health checks for every service. The public API check is `GET /healthz` on container port `8080`; it returns `200` only when PostgreSQL and Redis are reachable and otherwise returns `503`. The worker exposes an internal `GET /healthz` on port `8081` with the same dependency checks. PostgreSQL uses `pg_isready`, and Redis uses `redis-cli ping`.

Coolify Compose deployments read these checks directly from `compose.yaml` and do not use Coolify's standard application **Healthcheck** configuration page. After redeployment, inspect the individual Compose services: `api`, `worker`, `postgres`, and `redis` should all become healthy after their startup grace periods. Only the API should have a public domain.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_ROLE` | `api` | Run the `api` or `worker` process |
| `APP_ENV` | `development` | Use `production` to enforce production configuration |
| `DATABASE_URL` | local Postgres URL | Postgres connection string |
| `REDIS_ADDRESS` | `localhost:6379` | Redis address |
| `OIDC_ISSUER_URL` | empty | Authentik application's OIDC issuer URL |
| `OIDC_CLIENT_ID` | empty | Authentik provider client ID |
| `OIDC_CLIENT_SECRET` | empty | Authentik provider client secret |
| `OIDC_REDIRECT_URL` | empty | Public `/auth/callback` URL |
| `OIDC_ADMIN_GROUP` | `report-admins` | Authentik group granted template administration |
| `SESSION_SECRET` | empty | At least 32 characters used to sign browser sessions |
| `WORKER_CONCURRENCY` | CPU count | Concurrent render jobs |
| `RENDER_TIMEOUT` | `60s` | Per-report Typst timeout |
| `TYPST_ROOT` | `typst` | Shared Typst component directory |
| `REPORTS_DIRECTORY` | `data/reports` | Filesystem artifact root |
| `STORAGE_MASTER_KEY` | development-only fallback | Base64 encoding of exactly 32 bytes used to encrypt profile credentials; required in production and identical on API/workers |
| `RENDERER_VERSION` | `dev` | Audit version recorded on jobs |

Production still requires OIDC configuration to bootstrap and administer API clients. Keep the bucket private; report consumers should use the authenticated report API rather than direct object-store access.
