package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"reporting-server/internal/jobs"
	"reporting-server/internal/storagecontrol"
)

func (a *API) listReports(response http.ResponseWriter, request *http.Request) {
	limit, err := queryInt(request, "limit", 50)
	if err != nil || limit < 1 || limit > 200 {
		writeError(response, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
		return
	}
	offset, err := queryInt(request, "offset", 0)
	if err != nil || offset < 0 {
		writeError(response, http.StatusBadRequest, "invalid_offset", "offset must be zero or greater")
		return
	}
	status := strings.TrimSpace(request.URL.Query().Get("status"))
	if status != "" && status != "queued" && status != "processing" && status != "completed" && status != "failed" {
		writeError(response, http.StatusBadRequest, "invalid_status", "status must be queued, processing, completed, or failed")
		return
	}
	filter, err := reportListFilter(request, limit, offset, status)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	result, err := a.reports.List(request.Context(), filter)
	if err != nil {
		slog.Error("list report jobs", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not list report jobs")
		return
	}
	for index := range result.Items {
		if result.Items[index].Status == "completed" {
			result.Items[index].DownloadURL = "/v1/reports/" + result.Items[index].ID + "/download"
		}
	}
	writeJSON(response, http.StatusOK, result)
}

func reportListFilter(request *http.Request, limit, offset int, status string) (jobs.ListFilter, error) {
	query := request.URL.Query()
	filter := jobs.ListFilter{Template: strings.TrimSpace(query.Get("template")), Status: status, SampleID: strings.TrimSpace(query.Get("sampleId")), RequestedBy: strings.TrimSpace(query.Get("requestedBy")), Limit: limit, Offset: offset}
	if raw := strings.TrimSpace(query.Get("templateVersion")); raw != "" {
		version, err := strconv.Atoi(raw)
		if err != nil || version < 1 {
			return filter, errors.New("templateVersion must be a positive integer")
		}
		filter.TemplateVersion = &version
	}
	parseDate := func(name string) (*time.Time, error) {
		raw := strings.TrimSpace(query.Get(name))
		if raw == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("%s must be an RFC3339 timestamp", name)
		}
		return &parsed, nil
	}
	var err error
	if filter.CreatedFrom, err = parseDate("createdFrom"); err != nil {
		return filter, err
	}
	if filter.CreatedTo, err = parseDate("createdTo"); err != nil {
		return filter, err
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedFrom.Before(*filter.CreatedTo) {
		return filter, errors.New("createdFrom must be before createdTo")
	}
	return filter, nil
}

func (a *API) adminReportDetail(response http.ResponseWriter, request *http.Request) {
	detail, err := a.reports.Detail(request.Context(), request.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Report job was not found")
		return
	}
	if err != nil {
		slog.Error("get admin report detail", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not load report detail")
		return
	}
	if detail.Status == "completed" {
		detail.DownloadURL = "/v1/reports/" + detail.ID + "/download"
	}
	writeJSON(response, http.StatusOK, detail)
}

func (a *API) retryReport(response http.ResponseWriter, request *http.Request) {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if len(key) > 200 {
		writeError(response, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be at most 200 characters")
		return
	}
	job, replayed, err := a.reports.Retry(request.Context(), request.PathValue("id"), ulid.Make().String(), actor(request), key)
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Report job was not found")
		return
	}
	if errors.Is(err, jobs.ErrNotFailed) {
		writeError(response, http.StatusConflict, "report_not_failed", "Only failed reports can be retried")
		return
	}
	if errors.Is(err, jobs.ErrTemplateGone) {
		writeError(response, http.StatusConflict, "template_version_unavailable", "The original published template version is unavailable")
		return
	}
	if err != nil {
		slog.Error("retry report", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not retry report")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job": job, "idempotentReplay": replayed})
}

func (a *API) recoverReport(response http.ResponseWriter, request *http.Request) {
	err := a.reports.RecoverStale(request.Context(), request.PathValue("id"), actor(request), time.Now().UTC())
	if errors.Is(err, jobs.ErrNotRecoverable) {
		writeError(response, http.StatusConflict, "report_not_recoverable", "Report must be processing with an expired lease and a stale worker heartbeat")
		return
	}
	if err != nil {
		slog.Error("recover stale report", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not recover report")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"jobId": request.PathValue("id"), "status": "queued"})
}

func (a *API) operationsHealth(response http.ResponseWriter, request *http.Request) {
	health, err := a.reports.OperationsHealth(request.Context(), time.Now().UTC())
	if err != nil {
		slog.Error("load operations health", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not load operations health")
		return
	}
	queueStats, err := a.queue.QueueStats()
	if err != nil {
		slog.Warn("load Asynq queue stats", "error", err)
		writeJSON(response, http.StatusOK, map[string]any{"database": health, "queues": []any{}, "queueError": "Queue statistics are temporarily unavailable"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"database": health, "queues": queueStats})
}

func (a *API) downloadReport(response http.ResponseWriter, request *http.Request) {
	job, err := a.reports.Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Report job was not found")
		return
	}
	if err != nil {
		slog.Error("load report for download", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not load report job")
		return
	}
	if job.Status != "completed" || job.StorageKey == nil || job.SHA256 == nil {
		writeError(response, http.StatusConflict, "report_not_ready", "Report PDF is not available")
		return
	}
	preferredProfileID := ""
	if job.StorageProfileID != nil {
		preferredProfileID = *job.StorageProfileID
	}
	pdf, err := a.storage.ReadReport(request.Context(), job.ID, preferredProfileID, *job.StorageKey, *job.SHA256)
	if err != nil {
		slog.Error("read report artifact", "job_id", job.ID, "error", err)
		if errors.Is(err, storagecontrol.ErrIntegrity) {
			writeError(response, http.StatusInternalServerError, "artifact_integrity_error", "Stored report failed integrity verification")
			return
		}
		writeError(response, http.StatusServiceUnavailable, "storage_unavailable", "Could not retrieve report PDF")
		return
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(pdf))
	if digest != *job.SHA256 {
		slog.Error("report artifact hash mismatch", "job_id", job.ID, "expected", *job.SHA256, "actual", digest)
		writeError(response, http.StatusInternalServerError, "artifact_integrity_error", "Stored report failed integrity verification")
		return
	}
	filename := fmt.Sprintf("%s-v%d-%s.pdf", job.Template, job.TemplateVersion, job.ID)
	response.Header().Set("Content-Type", "application/pdf")
	response.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	response.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	response.Header().Set("Cache-Control", "private, no-store")
	response.Header().Set("ETag", `"`+digest+`"`)
	response.Header().Set("X-Content-SHA256", digest)
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := response.Write(pdf); err != nil {
		slog.Error("write report download", "job_id", job.ID, "error", err)
	}
}

func queryInt(request *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
