# Typst Report Service

## Goal

Build a self-hosted report-generation service for the LIMS that provides:

* Pixel-consistent PDF generation using **Typst**
* Editable and version-controlled report templates
* JSON/API-driven dynamic report data
* Durable queued report generation
* Cloud object storage for completed PDFs
* A simple browser interface for designing and managing templates
* Deployment as a single Git-backed **Coolify** application

## Architecture

```text
Next.js LIMS / Vercel
        │
        │ POST report request
        ▼
┌──────────────────────────┐
│      Go Report API       │
│  reports-api.example.com │
└────────────┬─────────────┘
             │
             │ enqueue
             ▼
┌──────────────────────────┐
│      Redis / Asynq       │
│      Durable Queue       │
└────────────┬─────────────┘
             │
             ▼
┌──────────────────────────┐
│       Go Worker(s)       │
│            │             │
│         Typst CLI        │
│            │             │
│            ▼             │
│          PDF             │
└────────────┬─────────────┘
             │
             ▼
       Cloudflare R2
```

A separate human-facing interface is exposed at:

```text
reports.example.com
```

This provides the **Report Template Manager**.

## Report Template Manager

The management UI is a lightweight web application rather than a custom visual document editor.

```text
┌──────────────────────────────────────────────────────────────┐
│ Certificate of Analysis                     Save | Publish   │
├────────────────────────────┬─────────────────────────────────┤
│ Typst Source               │ PDF Preview                     │
│                            │                                 │
│ #set page(...)             │   CERTIFICATE OF ANALYSIS       │
│                            │                                 │
│ Sample: #data.sample.id    │   Sample: 26000123              │
│                            │                                 │
│ #results-table(            │   Test           Result         │
│   data.results             │   APC            <10            │
│ )                          │   Salmonella     ND             │
│                            │                                 │
├────────────────────────────┴─────────────────────────────────┤
│ Test Data: [Multi-page COA ▼]        Compile ✓              │
└──────────────────────────────────────────────────────────────┘
```

Use **Monaco or CodeMirror** for Typst source editing.

The UI should support:

* Create/edit templates
* Live or on-demand PDF preview
* Sample/test datasets
* Draft templates
* Version history
* Approval/publishing
* Immutable published versions

Typst handles document layout, pagination, tables, fonts, headers, footers, etc. The application does **not** implement its own layout engine.

## Typst Templates

Reusable lab components can hide Typst complexity:

```typst
#import "common/lab.typ": *

#let data = json(bytes(sys.inputs.data))

#lab-report(
  title: "Certificate of Analysis",
)

#section("Sample Information")

#field("Sample ID", data.sample.id)
#field("Description", data.sample.description)

#section("Results")

#results-table(data.results)
```

Shared components live separately from individual report templates.

## Template Lifecycle

Production reports must always use immutable published templates.

```text
Draft v18
    │
    ├── Edit
    ├── Preview
    └── Test
         │
         ▼
      Approve
         │
         ▼
      Publish
         │
         ▼
      COA:v18
     (immutable)
```

Old versions remain available for audit/reproducibility.

## Report API

Example request:

```http
POST /v1/reports
```

```json
{
  "template": "certificate-of-analysis",
  "version": 18,
  "data": {
    "sample": {},
    "client": {},
    "results": []
  }
}
```

Immediate response:

```json
{
  "jobId": "01K5ABC...",
  "status": "queued"
}
```

Status:

```http
GET /v1/reports/01K5ABC...
```

Completed response:

```json
{
  "jobId": "01K5ABC...",
  "status": "completed",
  "storageKey": "reports/2026/report.pdf",
  "sha256": "...",
  "pages": 3
}
```

## Queue

Use **Redis + Asynq**.

The queue provides:

* Durable jobs
* Automatic retries
* Failure handling
* Timeouts
* Controlled concurrency
* Multiple workers
* Priority queues if needed

Example:

```text
500 requested reports

12 processing
488 queued
```

Workers can be horizontally scaled without changing the API.

## Persistent Job Records

Redis handles execution, but persistent job/audit information should live in Postgres.

```text
report_jobs

id
template_id
template_version
status
created_at
started_at
completed_at
attempts
error
storage_key
sha256
renderer_version
```

A generated report should record enough information to reproduce and audit it:

```text
Template Version
Renderer Version
Data Snapshot / Hash
PDF SHA-256
Generation Timestamp
User/System Responsible
Storage Key
```

## Storage

Use **Cloudflare R2** or another S3-compatible store for:

* Generated PDFs
* Published template versions
* Assets if appropriate

The report service should support both:

```text
Render → Upload → Return storage metadata
```

and:

```text
Render → Return application/pdf directly
```

## Deployment

Maintain everything in one Git repository:

```text
lims-report-server/
├── cmd/
│   └── server/
├── internal/
│   ├── api/
│   ├── queue/
│   ├── renderer/
│   ├── storage/
│   ├── templates/
│   └── auth/
├── web/
│   └── template-manager/
├── typst/
│   └── common/
│       ├── lab.typ
│       ├── tables.typ
│       └── styles.typ
├── fonts/
├── Dockerfile
├── compose.yaml
├── go.mod
└── go.sum
```

The web UI can be built as a small React/Vite application and embedded into the Go binary using `go:embed`.

## Docker / Coolify

The Compose stack contains:

```text
api       Go API + Template Manager
worker    Go Worker + Typst
redis     Redis/Asynq queue
```

Only the required HTTP service is publicly exposed.

Coolify:

```text
Git Repository
      │
      │ push
      ▼
   Coolify
      │
      ├── Build Go application
      ├── Build web UI
      ├── Install pinned Typst
      ├── Deploy API
      ├── Deploy worker(s)
      └── Maintain Redis volume
```

Secrets such as R2 credentials, database credentials and API keys are configured through Coolify environment variables.

## Reproducible Rendering

The Docker image should contain a **pinned Typst version and controlled fonts**.

```text
report-server:v1.7.2

Go application
Typst 0.x.x
Lab Typst library
Approved fonts
```

Do not depend on Typst or fonts installed on the VPS host.

This helps ensure the same template and data continue to produce consistent output.

## Recommended Stack

```text
Frontend / LIMS       Next.js + Vercel

Report Service        Go

Template Language     Typst

Template Editor       Monaco or CodeMirror

PDF Renderer          Typst CLI

Queue                 Asynq

Queue Storage         Redis

Metadata / Audit      Postgres

PDF Storage           Cloudflare R2

Authentication        Authentik / service API keys

Deployment            Docker Compose + Coolify

Source Control        Git
```

The result is a dedicated **LIMS report server** where Typst owns document layout, Go owns orchestration, Redis/Asynq owns reliable execution, R2 owns generated artifacts, and the LIMS only needs to submit structured data and track the resulting job.

