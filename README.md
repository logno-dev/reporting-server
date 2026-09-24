# Typst Report Service

Initial backend for durable, reproducible Typst report generation.

## Run locally

```sh
cp .env.example .env
docker compose up --build
```

Submit the seeded Certificate of Analysis template:

```sh
curl -X POST http://localhost:8080/v1/reports \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer rpt_live_...' \
  -d '{
    "template": "certificate-of-analysis",
    "version": 1,
    "data": {
      "sample": {"id": "26000123", "description": "Example sample"},
      "results": [
        {"test": "APC", "result": "<10"},
        {"test": "Salmonella", "result": "ND"}
      ]
    }
  }'
```

The response contains a `jobId`, resolved `templateVersion`, and `schemaHash`. Poll `GET /v1/reports/{jobId}` for status. To always select the latest published version, omit `version` from the request. Contract resolution, validation, job creation, and outbox creation share one database transaction, and the resolved numeric version is stored on the job so a later publication cannot change the queued report. Include a positive `version` to pin a specific published version explicitly.

LIMS clients can discover valid immutable templates before submitting a report:

```sh
curl -H 'Authorization: Bearer rpt_live_...' \
  'http://localhost:8080/v1/report-templates'
```

The catalog contains only published versions, identifies `latestVersion`, and includes `dataSchema` and `schemaHash` for every immutable version. Drafts, Typst source, storage keys, and editor-only metadata are never exposed through this machine endpoint.

### Schema-driven LIMS flow

1. Read `GET /v1/report-templates` and choose a concrete version and its `schemaHash`.
2. Build or validate the LIMS payload against that version's `dataSchema`.
3. Submit `template`, the numeric `version`, `schemaHash`, and `data`. A stale hash receives `409 template_contract_changed`; invalid data receives `422 data_validation_failed` with field paths.
4. Refresh the catalog and require an intentional mapping update when a contract changes.

Clients may omit `schemaHash` for compatibility, but all submissions are still validated. For controlled production integrations, pin both numeric `version` and `schemaHash`. Omitting `version` opts into a latest-published policy: the server atomically resolves and pins the version, but a newly published contract can make a previously valid payload fail. Existing versions published before contract support expose a permissive object schema and continue accepting object payloads.

Report submission uses a transactional Postgres outbox. Creating the report record and its queue intent is one database transaction, so Redis downtime does not lose work. API dispatchers lease pending rows and enqueue them with the report ULID as an idempotent Asynq task ID. Once Redis recovers, pending reports are delivered automatically without client resubmission.

Completed reports remain discoverable through Postgres even if a caller loses its original job ID:

```sh
curl -H 'Authorization: Bearer rpt_live_...' \
  'http://localhost:8080/v1/reports?status=completed&sampleId=26000123'

curl -H 'Authorization: Bearer rpt_live_...' \
  -o report.pdf \
  'http://localhost:8080/v1/reports/{jobId}/download'
```

`GET /v1/reports` supports `sampleId` (case-insensitive text match), `template`, exact `templateVersion`, `status`, exact `requestedBy` identity, RFC3339 `createdFrom`/`createdTo`, `limit`, and `offset` query parameters. Results include hashes, storage profile, renderer version, errors, attempt count, retry lineage, timestamps, schema hash, and a stable `downloadUrl` for completed reports. The immutable input payload is deliberately omitted. Downloads are served through the authenticated API, checked against the recorded SHA-256 digest, and never require clients to know bucket credentials or object keys.

## Template Manager

Open `http://localhost:8080` to use the embedded template workbench. It includes:

- CodeMirror editing for Typst source and JSON test data
- On-demand PDF compilation and preview
- Template and version navigation
- Draft creation and saving
- Dirty-state warnings, keyboard save/preview shortcuts, and draft discard
- Draft approval and immutable publishing
- Debounced contract analysis that adds missing fake-data fields, displays diagnostics and a schema hash, and blocks unsafe approval

The lifecycle is `draft -> approved -> published`. Published history opens read-only; use **Create editable draft** or **Open draft** to make changes. Approved versions are locked, and published versions cannot be modified by the application or directly in Postgres.

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
