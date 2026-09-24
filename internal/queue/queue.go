package queue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/oklog/ulid/v2"
	"reporting-server/internal/jobs"
	"reporting-server/internal/renderer"
	"reporting-server/internal/storagecontrol"
)

const reportTask = "report:render"
const migrationTask = "storage:migrate"

type reportPayload struct {
	JobID string `json:"jobId"`
}

type migrationPayload struct {
	MigrationID string `json:"migrationId"`
	ObjectID    string `json:"objectId"`
}

type Client struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	timeout   time.Duration
}

func NewClient(options asynq.RedisClientOpt, timeout time.Duration) *Client {
	return &Client{client: asynq.NewClient(options), inspector: asynq.NewInspector(options), timeout: timeout}
}

func (c *Client) Close() error {
	_ = c.inspector.Close()
	return c.client.Close()
}

func (c *Client) Enqueue(ctx context.Context, jobID string) error {
	payload, err := json.Marshal(reportPayload{JobID: jobID})
	if err != nil {
		return fmt.Errorf("encode report task: %w", err)
	}
	_, err = c.client.EnqueueContext(ctx, asynq.NewTask(reportTask, payload),
		asynq.TaskID(jobID), asynq.MaxRetry(5), asynq.Timeout(c.timeout), asynq.Queue("default"))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue report task: %w", err)
	}
	return nil
}

func (c *Client) EnqueueMigration(ctx context.Context, work storagecontrol.MigrationWork) error {
	payload, err := json.Marshal(migrationPayload{MigrationID: work.MigrationID, ObjectID: work.ObjectID})
	if err != nil {
		return err
	}
	taskID := fmt.Sprintf("storage-migration:%s:%s:%d", work.MigrationID, work.ObjectID, work.Attempts)
	_, err = c.client.EnqueueContext(ctx, asynq.NewTask(migrationTask, payload), asynq.TaskID(taskID), asynq.MaxRetry(0), asynq.Timeout(30*time.Minute), asynq.Queue("storage-migration"))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

func (c *Client) Ping() error {
	return c.client.Ping()
}

type QueueStats struct {
	Queue     string `json:"queue"`
	Pending   int    `json:"pending"`
	Active    int    `json:"active"`
	Scheduled int    `json:"scheduled"`
	Retry     int    `json:"retry"`
	Archived  int    `json:"archived"`
	LatencyMS int64  `json:"latencyMs"`
	Paused    bool   `json:"paused"`
}

func (c *Client) QueueStats() ([]QueueStats, error) {
	queues, err := c.inspector.Queues()
	if err != nil {
		return nil, err
	}
	exists := make(map[string]bool, len(queues))
	for _, name := range queues {
		exists[name] = true
	}

	result := make([]QueueStats, 0, 2)
	for _, name := range []string{"default", "storage-migration"} {
		if !exists[name] {
			result = append(result, QueueStats{Queue: name})
			continue
		}
		info, err := c.inspector.GetQueueInfo(name)
		if err != nil {
			return nil, err
		}
		result = append(result, QueueStats{Queue: name, Pending: info.Pending, Active: info.Active, Scheduled: info.Scheduled, Retry: info.Retry, Archived: info.Archived, LatencyMS: info.Latency.Milliseconds(), Paused: info.Paused})
	}
	return result, nil
}

type Dispatcher struct {
	owner   string
	client  *Client
	reports *jobs.Repository
	storage *storagecontrol.Repository
	lease   time.Duration
}

func NewDispatcher(client *Client, reports *jobs.Repository, storage *storagecontrol.Repository) *Dispatcher {
	return &Dispatcher{
		owner:   "dispatcher-" + ulid.Make().String(),
		client:  client,
		reports: reports, storage: storage,
		lease: 30 * time.Second,
	}
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		d.dispatch(ctx)
		d.dispatchMigrations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) dispatchMigrations(ctx context.Context) {
	for range 50 {
		work, err := d.storage.ClaimMigrationWork(ctx, 30*time.Minute)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("claim storage migration", "error", err)
			}
			return
		}
		if work == nil {
			return
		}
		if err := d.client.EnqueueMigration(ctx, *work); err != nil {
			_ = d.storage.ReleaseMigrationWork(ctx, work.MigrationID, work.ObjectID, "enqueue migration task: "+err.Error(), true)
			return
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context) {
	items, err := d.reports.ClaimOutbox(ctx, d.owner, 50, d.lease)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("claim report outbox", "error", err)
		}
		return
	}
	for _, item := range items {
		if err := d.client.Enqueue(ctx, item.JobID); err != nil {
			delay := time.Second << min(item.Attempts, 6)
			slog.Warn("report enqueue deferred", "job_id", item.JobID, "attempt", item.Attempts+1, "retry_in", delay, "error", err)
			if retryErr := d.reports.RetryOutbox(ctx, item.JobID, d.owner, err.Error(), delay); retryErr != nil && ctx.Err() == nil {
				slog.Error("release report outbox item", "job_id", item.JobID, "error", retryErr)
			}
			continue
		}
		if err := d.reports.CompleteOutbox(ctx, item.JobID, d.owner); err != nil && ctx.Err() == nil {
			slog.Error("complete report outbox item", "job_id", item.JobID, "error", err)
		}
	}
}

type Worker struct {
	server          *asynq.Server
	migrationServer *asynq.Server
	reports         *jobs.Repository
	renderer        *renderer.Renderer
	storage         *storagecontrol.Service
	version         string
	metadata        *storagecontrol.Repository
	workerID        string
	concurrency     int
	lease           time.Duration
	startedAt       time.Time
	currentJobs     atomic.Int64
	shutdownOnce    sync.Once
}

func NewWorker(options asynq.RedisClientOpt, concurrency int, reports *jobs.Repository, render *renderer.Renderer, store *storagecontrol.Service, metadata *storagecontrol.Repository, version string, lease time.Duration) *Worker {
	return &Worker{
		server: asynq.NewServer(options, asynq.Config{
			Concurrency: concurrency,
			Queues:      map[string]int{"default": 1},
		}),
		migrationServer: asynq.NewServer(options, asynq.Config{Concurrency: concurrency, Queues: map[string]int{"storage-migration": 1}}),
		reports:         reports, renderer: render, storage: store, metadata: metadata, version: version,
		workerID: "worker-" + ulid.Make().String(), concurrency: concurrency, lease: lease, startedAt: time.Now().UTC(),
	}
}

func (w *Worker) Run() error {
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	go w.runHeartbeat(heartbeatCtx)
	reportMux := asynq.NewServeMux()
	reportMux.HandleFunc(reportTask, w.handleReport)
	migrationMux := asynq.NewServeMux()
	migrationMux.HandleFunc(migrationTask, w.handleMigration)
	errors := make(chan error, 2)
	go func() { errors <- w.server.Run(reportMux) }()
	go func() { errors <- w.migrationServer.Run(migrationMux) }()
	err := <-errors
	w.Shutdown()
	return err
}

func (w *Worker) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.reports.Heartbeat(ctx, w.workerID, w.startedAt, w.concurrency, w.version, int(w.currentJobs.Load())); err != nil && ctx.Err() == nil {
			slog.Warn("worker heartbeat failed", "worker_id", w.workerID, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) handleMigration(ctx context.Context, task *asynq.Task) error {
	var payload migrationPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode migration task: %w: %v", asynq.SkipRetry, err)
	}
	work, active, err := w.metadata.GetMigrationWork(ctx, payload.MigrationID, payload.ObjectID)
	if err != nil {
		return fmt.Errorf("load migration work: %w", err)
	}
	if !active {
		return w.metadata.ReleaseMigrationWork(ctx, work.MigrationID, work.ObjectID, "migration is not running", true)
	}
	available, err := w.metadata.DestinationAvailable(ctx, work.ObjectID, work.DestinationProfileID)
	if err != nil {
		return w.failMigration(ctx, work, err, true)
	}
	if available {
		return w.metadata.CompleteMigrationWork(ctx, work, "", 0, true)
	}
	source, err := w.storage.Resolve(ctx, work.SourceProfileID)
	if err != nil {
		return w.failMigration(ctx, work, err, false)
	}
	target, err := w.storage.Resolve(ctx, work.DestinationProfileID)
	if err != nil {
		return w.failMigration(ctx, work, err, false)
	}
	contents, err := source.Get(ctx, work.StorageKey)
	if err != nil {
		return w.failMigration(ctx, work, err, true)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(contents))
	if work.ExpectedSHA256 != nil && digest != *work.ExpectedSHA256 {
		return w.failMigration(ctx, work, storagecontrol.ErrIntegrity, false)
	}
	if work.ExpectedSizeBytes != nil && int64(len(contents)) != *work.ExpectedSizeBytes {
		return w.failMigration(ctx, work, storagecontrol.ErrIntegrity, false)
	}
	result, err := target.Put(ctx, work.StorageKey, contents)
	if err != nil {
		return w.failMigration(ctx, work, err, true)
	}
	if result.Key != work.StorageKey || result.SHA256 != digest {
		return w.failMigration(ctx, work, storagecontrol.ErrIntegrity, false)
	}
	if err := w.metadata.CompleteMigrationWork(ctx, work, digest, int64(len(contents)), false); err != nil {
		return fmt.Errorf("complete migration item: %w", err)
	}
	return nil
}

func (w *Worker) failMigration(ctx context.Context, work storagecontrol.MigrationWork, cause error, transient bool) error {
	retry := transient && work.Attempts < 5
	if err := w.metadata.ReleaseMigrationWork(ctx, work.MigrationID, work.ObjectID, cause.Error(), retry); err != nil {
		return fmt.Errorf("persist migration failure: %w", err)
	}
	return nil
}

func (w *Worker) Shutdown() {
	w.shutdownOnce.Do(func() {
		w.server.Shutdown()
		w.migrationServer.Shutdown()
	})
}

func (w *Worker) handleReport(ctx context.Context, task *asynq.Task) error {
	var payload reportPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode report task: %w: %v", asynq.SkipRetry, err)
	}
	if payload.JobID == "" {
		return fmt.Errorf("job ID is required: %w", asynq.SkipRetry)
	}

	job, err := w.reports.Get(ctx, payload.JobID)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}
	if job.Status != "queued" {
		return nil
	}
	claimed, err := w.reports.MarkProcessing(ctx, payload.JobID, w.workerID, w.lease)
	if err != nil {
		return fmt.Errorf("mark report processing: %w", err)
	}
	if !claimed {
		return nil
	}
	w.currentJobs.Add(1)
	defer w.currentJobs.Add(-1)
	templateKey, err := w.reports.TemplateStorageKey(ctx, payload.JobID)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}
	preferredProfileID := ""
	if job.StorageProfileID != nil {
		preferredProfileID = *job.StorageProfileID
	}
	source, err := w.storage.ReadTemplate(ctx, job.TemplateID, job.TemplateVersion, preferredProfileID, templateKey)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}

	pdf, err := w.renderer.Render(ctx, string(source), job.Data)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}
	key := fmt.Sprintf("reports/%s/%s.pdf", time.Now().UTC().Format("2006/01/02"), payload.JobID)
	profileID, client, err := w.storage.WriteTarget(ctx, job.StorageProfileID)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}
	result, err := client.Put(ctx, key, pdf)
	if err != nil {
		return w.handleFailure(ctx, task, payload.JobID, err)
	}
	if err := w.reports.MarkCompleted(ctx, payload.JobID, w.workerID, profileID, result.Key, result.SHA256, int64(len(pdf)), w.version); err != nil {
		return fmt.Errorf("mark report completed: %w", err)
	}
	slog.Info("report generated", "job_id", payload.JobID, "storage_key", result.Key)
	return nil
}

func (w *Worker) handleFailure(ctx context.Context, task *asynq.Task, jobID string, cause error) error {
	retries, _ := asynq.GetRetryCount(ctx)
	maxRetries, _ := asynq.GetMaxRetry(ctx)
	if retries >= maxRetries {
		if err := w.reports.MarkFailed(ctx, jobID, w.workerID, cause.Error()); err != nil {
			slog.Error("could not persist report failure", "job_id", jobID, "error", err)
		}
	} else if err := w.reports.MarkRetry(ctx, jobID, w.workerID, cause.Error()); err != nil {
		slog.Error("could not persist report retry", "job_id", jobID, "error", err)
	}
	return cause
}
