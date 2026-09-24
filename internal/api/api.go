package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"reporting-server/internal/apiclients"
	"reporting-server/internal/auth"
	"reporting-server/internal/contract"
	"reporting-server/internal/jobs"
	reportqueue "reporting-server/internal/queue"
	"reporting-server/internal/renderer"
	"reporting-server/internal/storagecontrol"
	"reporting-server/internal/templates"
)

const maxRequestBody = 10 << 20

type QueueHealthChecker interface {
	Ping() error
	QueueStats() ([]reportqueue.QueueStats, error)
}

type HealthChecker interface {
	Ping(ctx context.Context) error
}

type API struct {
	reports         *jobs.Repository
	templates       *templates.Repository
	queue           QueueHealthChecker
	renderer        *renderer.Renderer
	storage         *storagecontrol.Service
	storageMetadata *storagecontrol.Repository
	db              HealthChecker
	auth            *auth.Authenticator
	apiClients      *apiclients.Service
}

func New(reports *jobs.Repository, templateRepository *templates.Repository, queue QueueHealthChecker, render *renderer.Renderer, store *storagecontrol.Service, storageMetadata *storagecontrol.Repository, clients *apiclients.Service, db HealthChecker, authenticator *auth.Authenticator, ui http.Handler) http.Handler {
	api := &API{reports: reports, templates: templateRepository, queue: queue, renderer: render, storage: store, storageMetadata: storageMetadata, apiClients: clients, db: db, auth: authenticator}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	authenticator.Register(mux)
	mux.Handle("POST /v1/reports", authenticator.AllowServiceOrAdmin(apiclients.ScopeReportsSubmit, http.HandlerFunc(api.createReport)))
	mux.Handle("GET /v1/reports", authenticator.AllowServiceOrUser(apiclients.ScopeReportsRead, http.HandlerFunc(api.listReports)))
	mux.Handle("GET /v1/reports/{id}", authenticator.AllowServiceOrUser(apiclients.ScopeReportsRead, http.HandlerFunc(api.getReport)))
	mux.Handle("GET /v1/reports/{id}/download", authenticator.AllowServiceOrUser(apiclients.ScopeReportsDownload, http.HandlerFunc(api.downloadReport)))
	mux.Handle("GET /v1/admin/reports/{id}", authenticator.RequireAdmin(http.HandlerFunc(api.adminReportDetail)))
	mux.Handle("POST /v1/admin/reports/{id}/retry", authenticator.RequireAdmin(http.HandlerFunc(api.retryReport)))
	mux.Handle("POST /v1/admin/reports/{id}/recover", authenticator.RequireAdmin(http.HandlerFunc(api.recoverReport)))
	mux.Handle("GET /v1/admin/operations/health", authenticator.RequireAdmin(http.HandlerFunc(api.operationsHealth)))
	mux.Handle("GET /v1/report-templates", authenticator.AllowServiceOrUser(apiclients.ScopeTemplatesRead, http.HandlerFunc(api.listPublishedTemplates)))
	mux.Handle("GET /v1/templates", authenticator.RequireUser(http.HandlerFunc(api.listTemplates)))
	mux.Handle("POST /v1/templates", authenticator.RequireAdmin(http.HandlerFunc(api.createTemplate)))
	mux.Handle("POST /v1/templates/{slug}/archive", authenticator.RequireAdmin(http.HandlerFunc(api.archiveTemplate)))
	mux.Handle("POST /v1/templates/{slug}/restore", authenticator.RequireAdmin(http.HandlerFunc(api.restoreTemplate)))
	mux.Handle("POST /v1/templates/preview", authenticator.RequireUser(http.HandlerFunc(api.previewTemplate)))
	mux.Handle("POST /v1/templates/analyze", authenticator.RequireUser(http.HandlerFunc(api.analyzeTemplate)))
	mux.Handle("GET /v1/templates/{slug}/versions/{version}", authenticator.RequireUser(http.HandlerFunc(api.getTemplateVersion)))
	mux.Handle("POST /v1/templates/{slug}/versions", authenticator.RequireAdmin(http.HandlerFunc(api.createTemplateVersion)))
	mux.Handle("PUT /v1/templates/{slug}/versions/{version}", authenticator.RequireAdmin(http.HandlerFunc(api.updateTemplateVersion)))
	mux.Handle("DELETE /v1/templates/{slug}/versions/{version}", authenticator.RequireAdmin(http.HandlerFunc(api.deleteTemplateVersion)))
	mux.Handle("POST /v1/templates/{slug}/versions/{version}/approve", authenticator.RequireAdmin(http.HandlerFunc(api.approveTemplateVersion)))
	mux.Handle("POST /v1/templates/{slug}/versions/{version}/publish", authenticator.RequireAdmin(http.HandlerFunc(api.publishTemplateVersion)))
	mux.Handle("GET /v1/storage/profiles", authenticator.RequireAdmin(http.HandlerFunc(api.listStorageProfiles)))
	mux.Handle("POST /v1/storage/profiles", authenticator.RequireAdmin(http.HandlerFunc(api.createStorageProfile)))
	mux.Handle("POST /v1/storage/profiles/{id}/test", authenticator.RequireAdmin(http.HandlerFunc(api.testStorageProfile)))
	mux.Handle("GET /v1/storage/default", authenticator.RequireAdmin(http.HandlerFunc(api.getStorageDefault)))
	mux.Handle("PUT /v1/storage/default", authenticator.RequireAdmin(http.HandlerFunc(api.setStorageDefault)))
	mux.Handle("GET /v1/storage/migrations", authenticator.RequireAdmin(http.HandlerFunc(api.listStorageMigrations)))
	mux.Handle("POST /v1/storage/migrations", authenticator.RequireAdmin(http.HandlerFunc(api.createStorageMigration)))
	mux.Handle("GET /v1/storage/migrations/{id}", authenticator.RequireAdmin(http.HandlerFunc(api.getStorageMigration)))
	mux.Handle("POST /v1/storage/migrations/{id}/{action}", authenticator.RequireAdmin(http.HandlerFunc(api.storageMigrationAction)))
	mux.Handle("GET /v1/api-clients", authenticator.RequireAdmin(http.HandlerFunc(api.listAPIClients)))
	mux.Handle("POST /v1/api-clients", authenticator.RequireAdmin(http.HandlerFunc(api.createAPIClient)))
	mux.Handle("GET /v1/api-clients/{id}", authenticator.RequireAdmin(http.HandlerFunc(api.getAPIClient)))
	mux.Handle("POST /v1/api-clients/{id}/disable", authenticator.RequireAdmin(http.HandlerFunc(api.disableAPIClient)))
	mux.Handle("GET /v1/api-clients/{id}/keys", authenticator.RequireAdmin(http.HandlerFunc(api.listAPIKeys)))
	mux.Handle("POST /v1/api-clients/{id}/keys", authenticator.RequireAdmin(http.HandlerFunc(api.issueAPIKey)))
	mux.Handle("POST /v1/api-clients/{id}/keys/{keyId}/rotate", authenticator.RequireAdmin(http.HandlerFunc(api.rotateAPIKey)))
	mux.Handle("POST /v1/api-clients/{id}/keys/{keyId}/revoke", authenticator.RequireAdmin(http.HandlerFunc(api.revokeAPIKey)))
	mux.HandleFunc("/v1/", func(response http.ResponseWriter, _ *http.Request) {
		writeError(response, http.StatusNotFound, "not_found", "API endpoint was not found")
	})
	mux.Handle("/", ui)
	return api.recover(api.logRequests(mux))
}

var validSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type createReportRequest struct {
	Template   string          `json:"template"`
	Version    *int            `json:"version"`
	SchemaHash *string         `json:"schemaHash"`
	Data       json.RawMessage `json:"data"`
}

func (a *API) createReport(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input createReportRequest
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "invalid_request", "Request body must contain one JSON object")
		return
	}
	input.Template = strings.TrimSpace(input.Template)
	if input.SchemaHash != nil {
		trimmed := strings.TrimSpace(*input.SchemaHash)
		input.SchemaHash = &trimmed
	}
	if input.Template == "" || (input.Version != nil && *input.Version < 1) || len(input.Data) == 0 || string(input.Data) == "null" {
		writeError(response, http.StatusBadRequest, "invalid_request", "template and data are required; version must be positive when provided")
		return
	}
	if len(input.Data) > contract.MaxDataBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "Report data exceeds the 2 MiB limit")
		return
	}

	jobID := ulid.Make().String()
	resolved, err := a.reports.Create(request.Context(), jobID, input.Template, input.Version, input.SchemaHash, input.Data, actor(request))
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(response, http.StatusUnprocessableEntity, "template_not_found", "Published template version was not found")
		return
	} else if errors.Is(err, jobs.ErrContractChanged) {
		writeError(response, http.StatusConflict, "template_contract_changed", "Published template contract does not match schemaHash")
		return
	} else {
		var validation *jobs.ValidationFailure
		if errors.As(err, &validation) {
			writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "data_validation_failed", "message": validation.Error(), "details": validation.Errors}})
			return
		}
	}
	if err != nil {
		slog.Error("create report job", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not create report job")
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{
		"jobId": jobID, "status": "queued", "template": input.Template, "templateVersion": resolved.Version, "schemaHash": resolved.SchemaHash,
	})
}

func (a *API) getReport(response http.ResponseWriter, request *http.Request) {
	job, err := a.reports.Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Report job was not found")
		return
	}
	if err != nil {
		slog.Error("get report job", "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not load report job")
		return
	}
	if job.Status == "completed" {
		job.DownloadURL = "/v1/reports/" + job.ID + "/download"
	}
	writeJSON(response, http.StatusOK, job)
}

func (a *API) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		writeError(response, http.StatusServiceUnavailable, "unhealthy", "Database is unavailable")
		return
	}
	if err := a.queue.Ping(); err != nil {
		writeError(response, http.StatusServiceUnavailable, "unhealthy", "Report queue is unavailable")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		slog.Info("http request", "method", request.Method, "path", request.URL.Path, "duration", time.Since(started))
	})
}

func (a *API) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				slog.Error("http panic", "error", fmt.Sprint(value))
				writeError(response, http.StatusInternalServerError, "internal_error", "Internal server error")
			}
		}()
		next.ServeHTTP(response, request)
	})
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}
