package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"reporting-server/internal/contract"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrContractChanged = errors.New("template contract changed")
	ErrNotFailed       = errors.New("report is not failed")
	ErrTemplateGone    = errors.New("original published template version is unavailable")
	ErrNotRecoverable  = errors.New("report is not safely recoverable")
)

type ValidationFailure struct{ Errors []contract.ValidationError }

func (e *ValidationFailure) Error() string { return "report data failed template contract validation" }

type CreateResult struct {
	Version    int
	SchemaHash string
}

type Job struct {
	ID               string          `json:"jobId"`
	TemplateID       int64           `json:"-"`
	Template         string          `json:"template"`
	TemplateVersion  int             `json:"templateVersion"`
	Status           string          `json:"status"`
	Data             json.RawMessage `json:"-"`
	DataSHA256       string          `json:"dataSha256"`
	RequestedBy      *string         `json:"requestedBy,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	StartedAt        *time.Time      `json:"startedAt,omitempty"`
	CompletedAt      *time.Time      `json:"completedAt,omitempty"`
	Attempts         int             `json:"attempts"`
	Error            *string         `json:"error,omitempty"`
	StorageKey       *string         `json:"storageKey,omitempty"`
	StorageProfileID *string         `json:"-"`
	StorageProfile   *string         `json:"storageProfile,omitempty"`
	SHA256           *string         `json:"sha256,omitempty"`
	Pages            *int            `json:"pages,omitempty"`
	RendererVersion  *string         `json:"rendererVersion,omitempty"`
	DownloadURL      string          `json:"downloadUrl,omitempty"`
	SchemaHash       string          `json:"schemaHash"`
	RetryParentID    *string         `json:"retryParentId,omitempty"`
	RetryChildren    []string        `json:"retryChildren"`
	ProcessingWorker *string         `json:"processingWorkerId,omitempty"`
	LeaseUntil       *time.Time      `json:"processingLeaseUntil,omitempty"`
}

type ListFilter struct {
	Template        string
	TemplateVersion *int
	Status          string
	SampleID        string
	RequestedBy     string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
	Limit           int
	Offset          int
}

type ListResult struct {
	Items   []Job `json:"items"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"hasMore"`
}

type OutboxItem struct {
	JobID    string
	Attempts int
}

type AttemptEvent struct {
	ID         int64     `json:"id"`
	Attempt    int       `json:"attempt"`
	Type       string    `json:"type"`
	WorkerID   *string   `json:"workerId,omitempty"`
	Actor      *string   `json:"actor,omitempty"`
	Message    *string   `json:"message,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

type Detail struct {
	Job
	AttemptsTimeline []AttemptEvent `json:"attemptTimeline"`
}

type WorkerHeartbeat struct {
	WorkerID        string    `json:"workerId"`
	StartedAt       time.Time `json:"startedAt"`
	LastSeenAt      time.Time `json:"lastSeenAt"`
	Concurrency     int       `json:"concurrency"`
	RendererVersion string    `json:"rendererVersion"`
	CurrentJobs     int       `json:"currentJobs"`
	AgeSeconds      int64     `json:"ageSeconds"`
	Online          bool      `json:"online"`
}

type OperationsHealth struct {
	Workers           []WorkerHeartbeat `json:"workers"`
	ReportCounts      map[string]int64  `json:"reportCounts"`
	OldestQueuedAt    *time.Time        `json:"oldestQueuedAt,omitempty"`
	OldestQueuedAge   *int64            `json:"oldestQueuedAgeSeconds,omitempty"`
	OutboxPending     int64             `json:"outboxPending"`
	MigrationsPending int64             `json:"migrationsPending"`
	MigrationsRunning int64             `json:"migrationsRunning"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, id, template string, version *int, schemaHash *string, data json.RawMessage, requestedBy string) (CreateResult, error) {
	digest := sha256.Sum256(data)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return CreateResult{}, fmt.Errorf("begin report job creation: %w", err)
	}
	defer tx.Rollback(ctx)
	var templateID int64
	var resolvedVersion int
	var resolvedHash string
	var dataSchema json.RawMessage
	err = tx.QueryRow(ctx, `SELECT t.id, tv.version, tv.schema_sha256, tv.data_schema FROM templates t JOIN LATERAL (SELECT version, schema_sha256, data_schema FROM template_versions WHERE template_id = t.id AND status = 'published' AND ($2::integer IS NULL OR version = $2) ORDER BY version DESC LIMIT 1) tv ON true WHERE t.slug = $1`, template, version).Scan(&templateID, &resolvedVersion, &resolvedHash, &dataSchema)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, ErrNotFound
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("resolve report template: %w", err)
	}
	if schemaHash != nil && *schemaHash != resolvedHash {
		return CreateResult{}, ErrContractChanged
	}
	validationErrors, err := contract.Validate(dataSchema, data)
	if err != nil {
		return CreateResult{}, fmt.Errorf("validate report data: %w", err)
	}
	if len(validationErrors) > 0 {
		return CreateResult{}, &ValidationFailure{Errors: validationErrors}
	}
	command := `INSERT INTO report_jobs (id, template_id, template_version, status, data, data_sha256, requested_by, storage_profile_id) SELECT $1, $2, $3, 'queued', $4, $5, NULLIF($6, ''), profile_id FROM storage_defaults WHERE singleton`
	tag, err := tx.Exec(ctx, command, id, templateID, resolvedVersion, data, hex.EncodeToString(digest[:]), requestedBy)
	if err != nil {
		return CreateResult{}, fmt.Errorf("insert report job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return CreateResult{}, errors.New("default storage profile is not configured")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO report_outbox (job_id) VALUES ($1)`, id); err != nil {
		return CreateResult{}, fmt.Errorf("insert report outbox item: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, fmt.Errorf("commit report job creation: %w", err)
	}
	return CreateResult{Version: resolvedVersion, SchemaHash: resolvedHash}, nil
}

func (r *Repository) ClaimOutbox(ctx context.Context, owner string, limit int, lease time.Duration) ([]OutboxItem, error) {
	const query = `
		WITH candidates AS (
			SELECT job_id FROM report_outbox
			WHERE available_at <= now() AND (locked_until IS NULL OR locked_until < now())
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE report_outbox o
		SET locked_by = $1, locked_until = now() + make_interval(secs => $3)
		FROM candidates c
		WHERE o.job_id = c.job_id
		RETURNING o.job_id, o.attempts`
	rows, err := r.pool.Query(ctx, query, owner, limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim report outbox: %w", err)
	}
	defer rows.Close()
	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		if err := rows.Scan(&item.JobID, &item.Attempts); err != nil {
			return nil, fmt.Errorf("scan report outbox: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) CompleteOutbox(ctx context.Context, jobID, owner string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM report_outbox WHERE job_id = $1 AND locked_by = $2`, jobID, owner)
	if err != nil {
		return fmt.Errorf("complete report outbox: %w", err)
	}
	return nil
}

func (r *Repository) RetryOutbox(ctx context.Context, jobID, owner, message string, delay time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE report_outbox
		SET attempts = attempts + 1, available_at = now() + make_interval(secs => $4),
		    locked_by = NULL, locked_until = NULL, last_error = $3
		WHERE job_id = $1 AND locked_by = $2`, jobID, owner, message, delay.Seconds())
	if err != nil {
		return fmt.Errorf("retry report outbox: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (Job, error) {
	const query = `
		SELECT j.id, j.template_id, t.slug, j.template_version, j.status, j.data, j.data_sha256, j.requested_by, j.created_at, j.started_at,
		       j.completed_at, j.attempts, j.error, j.storage_key, j.storage_profile_id, j.sha256, j.pages, j.renderer_version,
		       sp.name, tv.schema_sha256, j.retry_parent_id, j.processing_worker_id, j.processing_lease_until,
		       COALESCE(array_agg(c.id::text ORDER BY c.created_at) FILTER (WHERE c.id IS NOT NULL), ARRAY[]::text[])
		FROM report_jobs j JOIN templates t ON t.id = j.template_id
		JOIN template_versions tv ON tv.template_id=j.template_id AND tv.version=j.template_version
		LEFT JOIN storage_profiles sp ON sp.id=j.storage_profile_id
		LEFT JOIN report_jobs c ON c.retry_parent_id=j.id
		WHERE j.id = $1 GROUP BY j.id,t.id,tv.template_id,tv.version,sp.id`
	var job Job
	err := scanJob(r.pool.QueryRow(ctx, query, id), &job)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("get report job: %w", err)
	}
	return job, nil
}

func (r *Repository) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	query := `
		SELECT j.id, j.template_id, t.slug, j.template_version, j.status, j.data, j.data_sha256, j.requested_by, j.created_at, j.started_at,
		       j.completed_at, j.attempts, j.error, j.storage_key, j.storage_profile_id, j.sha256, j.pages, j.renderer_version,
		       sp.name, tv.schema_sha256, j.retry_parent_id, j.processing_worker_id, j.processing_lease_until,
		       COALESCE(array_agg(c.id::text ORDER BY c.created_at) FILTER (WHERE c.id IS NOT NULL), ARRAY[]::text[])
		FROM report_jobs j JOIN templates t ON t.id = j.template_id
		JOIN template_versions tv ON tv.template_id=j.template_id AND tv.version=j.template_version
		LEFT JOIN storage_profiles sp ON sp.id=j.storage_profile_id
		LEFT JOIN report_jobs c ON c.retry_parent_id=j.id`
	var conditions []string
	var arguments []any
	addCondition := func(condition string, value any) {
		arguments = append(arguments, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(arguments)))
	}
	if filter.Template != "" {
		addCondition("t.slug = $%d", filter.Template)
	}
	if filter.TemplateVersion != nil {
		addCondition("j.template_version = $%d", *filter.TemplateVersion)
	}
	if filter.Status != "" {
		addCondition("j.status = $%d", filter.Status)
	}
	if filter.SampleID != "" {
		addCondition("j.data::text ILIKE '%%' || $%d || '%%'", filter.SampleID)
	}
	if filter.RequestedBy != "" {
		addCondition("j.requested_by = $%d", filter.RequestedBy)
	}
	if filter.CreatedFrom != nil {
		addCondition("j.created_at >= $%d", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		addCondition("j.created_at < $%d", *filter.CreatedTo)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " GROUP BY j.id,t.id,tv.template_id,tv.version,sp.id"
	arguments = append(arguments, filter.Limit+1, filter.Offset)
	query += fmt.Sprintf(" ORDER BY j.created_at DESC, j.id DESC LIMIT $%d OFFSET $%d", len(arguments)-1, len(arguments))

	rows, err := r.pool.Query(ctx, query, arguments...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list report jobs: %w", err)
	}
	defer rows.Close()
	items := make([]Job, 0, filter.Limit+1)
	for rows.Next() {
		var job Job
		if err := scanJob(rows, &job); err != nil {
			return ListResult{}, fmt.Errorf("scan report job: %w", err)
		}
		items = append(items, job)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("list report jobs: %w", err)
	}
	hasMore := len(items) > filter.Limit
	if hasMore {
		items = items[:filter.Limit]
	}
	return ListResult{Items: items, Limit: filter.Limit, Offset: filter.Offset, HasMore: hasMore}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner, job *Job) error {
	return row.Scan(
		&job.ID, &job.TemplateID, &job.Template, &job.TemplateVersion, &job.Status, &job.Data, &job.DataSHA256,
		&job.RequestedBy, &job.CreatedAt, &job.StartedAt, &job.CompletedAt, &job.Attempts,
		&job.Error, &job.StorageKey, &job.StorageProfileID, &job.SHA256, &job.Pages, &job.RendererVersion,
		&job.StorageProfile, &job.SchemaHash, &job.RetryParentID, &job.ProcessingWorker, &job.LeaseUntil, &job.RetryChildren,
	)
}

func (r *Repository) TemplateStorageKey(ctx context.Context, jobID string) (string, error) {
	const query = `
		SELECT tv.storage_key FROM report_jobs j
		JOIN template_versions tv ON tv.template_id = j.template_id AND tv.version = j.template_version
		WHERE j.id = $1 AND tv.status = 'published'`
	var storageKey string
	if err := r.pool.QueryRow(ctx, query, jobID).Scan(&storageKey); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("get template storage key: %w", err)
	}
	return storageKey, nil
}

func (r *Repository) MarkProcessing(ctx context.Context, id, workerID string, lease time.Duration) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var attempt int
	err = tx.QueryRow(ctx, `
		UPDATE report_jobs SET status='processing',started_at=COALESCE(started_at,now()),completed_at=NULL,
		attempts=attempts+1,error=NULL,processing_worker_id=$2,
		processing_lease_until=now()+make_interval(secs=>$3)
		WHERE id=$1 AND status='queued'
		RETURNING attempts`, id, workerID, lease.Seconds()).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO report_attempt_events(report_job_id,attempt,event_type,worker_id) VALUES($1,$2,'started',$3)`, id, attempt, workerID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (r *Repository) MarkCompleted(ctx context.Context, id, workerID, profileID, storageKey, digest string, size int64, rendererVersion string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin report completion: %w", err)
	}
	defer tx.Rollback(ctx)
	objectID := ulid.Make().String()
	if err := tx.QueryRow(ctx, `
		INSERT INTO storage_objects (id, object_type, logical_key, report_job_id, sha256, size_bytes)
		VALUES ($1, 'report', $2, $3, $4, $5)
		ON CONFLICT (report_job_id) DO UPDATE
		SET logical_key = EXCLUDED.logical_key, sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes
		RETURNING id`, objectID, storageKey, id, digest, size).Scan(&objectID); err != nil {
		return fmt.Errorf("upsert report storage object: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO storage_object_placements (id, object_id, profile_id, storage_key, state, sha256, size_bytes, verified_at)
		VALUES ($1, $2, $3, $4, 'available', $5, $6, now())
		ON CONFLICT (object_id, profile_id) DO UPDATE
		SET storage_key = EXCLUDED.storage_key, state = 'available', sha256 = EXCLUDED.sha256,
		    size_bytes = EXCLUDED.size_bytes, verified_at = now(), error = NULL`,
		ulid.Make().String(), objectID, profileID, storageKey, digest, size); err != nil {
		return fmt.Errorf("upsert report placement: %w", err)
	}
	var attempt int
	if err := tx.QueryRow(ctx, `UPDATE report_jobs SET status='completed',completed_at=now(),storage_key=$3,sha256=$4,renderer_version=$5,error=NULL,processing_worker_id=NULL,processing_lease_until=NULL WHERE id=$1 AND status='processing' AND processing_worker_id=$2 RETURNING attempts`, id, workerID, storageKey, digest, rendererVersion).Scan(&attempt); err != nil {
		return fmt.Errorf("mark report completed: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO report_attempt_events(report_job_id,attempt,event_type,worker_id) VALUES($1,$2,'completed',$3)`, id, attempt, workerID); err != nil {
		return fmt.Errorf("record report completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit report completion: %w", err)
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, id, workerID, message string) error {
	return r.finishAttempt(ctx, id, workerID, message, "failed", "failed")
}

func (r *Repository) MarkRetry(ctx context.Context, id, workerID, message string) error {
	return r.finishAttempt(ctx, id, workerID, message, "queued", "retry")
}

func (r *Repository) finishAttempt(ctx context.Context, id, workerID, message, status, eventType string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempt int
	err = tx.QueryRow(ctx, `UPDATE report_jobs SET status=$3,completed_at=CASE WHEN $3='failed' THEN now() ELSE NULL END,error=$4,processing_worker_id=NULL,processing_lease_until=NULL WHERE id=$1 AND processing_worker_id=$2 RETURNING attempts`, id, workerID, status, message).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO report_attempt_events(report_job_id,attempt,event_type,worker_id,message) VALUES($1,$2,$3,$4,$5)`, id, attempt, eventType, workerID, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) Detail(ctx context.Context, id string) (Detail, error) {
	job, err := r.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id,attempt,event_type,worker_id,actor,message,occurred_at FROM report_attempt_events WHERE report_job_id=$1 ORDER BY occurred_at,id`, id)
	if err != nil {
		return Detail{}, fmt.Errorf("list report attempt events: %w", err)
	}
	defer rows.Close()
	detail := Detail{Job: job, AttemptsTimeline: []AttemptEvent{}}
	for rows.Next() {
		var event AttemptEvent
		if err := rows.Scan(&event.ID, &event.Attempt, &event.Type, &event.WorkerID, &event.Actor, &event.Message, &event.OccurredAt); err != nil {
			return Detail{}, err
		}
		detail.AttemptsTimeline = append(detail.AttemptsTimeline, event)
	}
	return detail, rows.Err()
}

// Retry creates a new immutable job snapshot; it never changes the failed parent.
func (r *Repository) Retry(ctx context.Context, parentID, newID, actor, idempotencyKey string) (Job, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM report_jobs WHERE id=$1 FOR UPDATE`, parentID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, ErrNotFound
	} else if err != nil {
		return Job{}, false, err
	}
	if status != "failed" {
		return Job{}, false, ErrNotFailed
	}
	if idempotencyKey != "" {
		var existing string
		err := tx.QueryRow(ctx, `SELECT id FROM report_jobs WHERE retry_parent_id=$1 AND requested_by=$2 AND retry_idempotency_key=$3`, parentID, actor, idempotencyKey).Scan(&existing)
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return Job{}, false, err
			}
			job, err := r.Get(ctx, existing)
			return job, true, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Job{}, false, err
		}
	}
	command := `
		INSERT INTO report_jobs(id,template_id,template_version,status,data,data_sha256,requested_by,storage_profile_id,retry_parent_id,retry_idempotency_key)
		SELECT $2,j.template_id,j.template_version,'queued',j.data,j.data_sha256,$3,d.profile_id,j.id,NULLIF($4,'')
		FROM report_jobs j JOIN template_versions tv ON tv.template_id=j.template_id AND tv.version=j.template_version AND tv.status='published'
		JOIN storage_defaults d ON d.singleton WHERE j.id=$1`
	tag, err := tx.Exec(ctx, command, parentID, newID, actor, idempotencyKey)
	if err != nil {
		return Job{}, false, fmt.Errorf("create report retry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Job{}, false, ErrTemplateGone
	}
	if _, err := tx.Exec(ctx, `INSERT INTO report_outbox(job_id) VALUES($1)`, newID); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, err
	}
	job, err := r.Get(ctx, newID)
	return job, false, err
}

func (r *Repository) Heartbeat(ctx context.Context, workerID string, startedAt time.Time, concurrency int, rendererVersion string, currentJobs int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO worker_heartbeats(worker_id,started_at,last_seen_at,concurrency,renderer_version,current_jobs)
		VALUES($1,$2,now(),$3,$4,$5)
		ON CONFLICT(worker_id) DO UPDATE SET last_seen_at=now(),concurrency=EXCLUDED.concurrency,renderer_version=EXCLUDED.renderer_version,current_jobs=EXCLUDED.current_jobs`, workerID, startedAt, concurrency, rendererVersion, currentJobs)
	return err
}

func HeartbeatOnline(lastSeen, now time.Time) bool {
	return now.Sub(lastSeen) <= 30*time.Second
}

func (r *Repository) OperationsHealth(ctx context.Context, now time.Time) (OperationsHealth, error) {
	health := OperationsHealth{Workers: []WorkerHeartbeat{}, ReportCounts: map[string]int64{"queued": 0, "processing": 0, "completed": 0, "failed": 0}}
	rows, err := r.pool.Query(ctx, `SELECT worker_id,started_at,last_seen_at,concurrency,renderer_version,current_jobs FROM worker_heartbeats ORDER BY last_seen_at DESC`)
	if err != nil {
		return health, err
	}
	for rows.Next() {
		var worker WorkerHeartbeat
		if err := rows.Scan(&worker.WorkerID, &worker.StartedAt, &worker.LastSeenAt, &worker.Concurrency, &worker.RendererVersion, &worker.CurrentJobs); err != nil {
			rows.Close()
			return health, err
		}
		worker.AgeSeconds = max(0, int64(now.Sub(worker.LastSeenAt).Seconds()))
		worker.Online = HeartbeatOnline(worker.LastSeenAt, now)
		health.Workers = append(health.Workers, worker)
	}
	rows.Close()
	rows, err = r.pool.Query(ctx, `SELECT status,count(*) FROM report_jobs GROUP BY status`)
	if err != nil {
		return health, err
	}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return health, err
		}
		health.ReportCounts[status] = count
	}
	rows.Close()
	if err := r.pool.QueryRow(ctx, `SELECT min(created_at) FROM report_jobs WHERE status='queued'`).Scan(&health.OldestQueuedAt); err != nil {
		return health, err
	}
	if health.OldestQueuedAt != nil {
		age := max(0, int64(now.Sub(*health.OldestQueuedAt).Seconds()))
		health.OldestQueuedAge = &age
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM report_outbox`).Scan(&health.OutboxPending); err != nil {
		return health, err
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state='running') FROM storage_migration_items`).Scan(&health.MigrationsPending, &health.MigrationsRunning); err != nil {
		return health, err
	}
	return health, nil
}

func (r *Repository) RecoverStale(ctx context.Context, id, actor string, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var attempt int
	var workerID string
	err = tx.QueryRow(ctx, `
		SELECT attempts,processing_worker_id FROM report_jobs j
		WHERE id=$1 AND status='processing' AND processing_lease_until < $2
		AND NOT EXISTS(SELECT 1 FROM worker_heartbeats w WHERE w.worker_id=j.processing_worker_id AND w.last_seen_at >= $2 - interval '30 seconds')
		FOR UPDATE`, id, now).Scan(&attempt, &workerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotRecoverable
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE report_jobs SET status='queued',completed_at=NULL,error='Recovered after expired worker lease',processing_worker_id=NULL,processing_lease_until=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO report_outbox(job_id) VALUES($1) ON CONFLICT(job_id) DO UPDATE SET available_at=now(),locked_by=NULL,locked_until=NULL,last_error=NULL`, id); err != nil {
		return err
	}
	message := "expired lease held by " + workerID
	if _, err := tx.Exec(ctx, `INSERT INTO report_attempt_events(report_job_id,attempt,event_type,actor,message) VALUES($1,$2,'recovered',$3,$4)`, id, attempt, actor, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
