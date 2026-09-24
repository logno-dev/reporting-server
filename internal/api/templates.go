package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"reporting-server/internal/auth"
	"reporting-server/internal/contract"
	"reporting-server/internal/templates"
)

type templateInput struct {
	Slug       string          `json:"slug"`
	Name       string          `json:"name"`
	Source     string          `json:"source"`
	SampleData json.RawMessage `json:"sampleData"`
}

type versionInput struct {
	Source     string          `json:"source"`
	SampleData json.RawMessage `json:"sampleData"`
}

func (a *API) listTemplates(response http.ResponseWriter, request *http.Request) {
	result, err := a.templates.List(request.Context())
	if err != nil {
		templateInternalError(response, "list templates", err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) listPublishedTemplates(response http.ResponseWriter, request *http.Request) {
	result, err := a.templates.PublishedCatalog(request.Context())
	if err != nil {
		templateInternalError(response, "list published template catalog", err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

func (a *API) createTemplate(response http.ResponseWriter, request *http.Request) {
	var input templateInput
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Slug = strings.TrimSpace(input.Slug)
	input.Name = strings.TrimSpace(input.Name)
	if !validSlug.MatchString(input.Slug) || input.Name == "" || strings.TrimSpace(input.Source) == "" || !validSampleData(input.SampleData) {
		writeError(response, http.StatusBadRequest, "invalid_request", "slug, name, source, and a JSON object sampleData are required")
		return
	}
	analysis, err := contract.Analyze(input.Source, input.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	result, err := a.templates.Create(request.Context(), input.Slug, input.Name, input.Source, analysis.SampleData, analysis.DataSchema, analysis.SchemaHash, actor(request))
	if errors.Is(err, templates.ErrConflict) {
		writeError(response, http.StatusConflict, "template_conflict", "Template slug already exists")
		return
	}
	if err != nil {
		templateInternalError(response, "create template", err)
		return
	}
	writeJSON(response, http.StatusCreated, result)
}

func (a *API) getTemplateVersion(response http.ResponseWriter, request *http.Request) {
	version, ok := pathVersion(response, request)
	if !ok {
		return
	}
	result, err := a.templates.GetVersion(request.Context(), request.PathValue("slug"), version)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Template version was not found")
		return
	}
	if err != nil {
		templateInternalError(response, "get template version", err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) createTemplateVersion(response http.ResponseWriter, request *http.Request) {
	var input versionInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if strings.TrimSpace(input.Source) == "" || !validSampleData(input.SampleData) {
		writeError(response, http.StatusBadRequest, "invalid_request", "source and a JSON object sampleData are required")
		return
	}
	analysis, err := contract.Analyze(input.Source, input.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	result, err := a.templates.CreateVersion(request.Context(), request.PathValue("slug"), input.Source, analysis.SampleData, analysis.DataSchema, analysis.SchemaHash, actor(request))
	handleVersionWrite(response, result, err, http.StatusCreated)
}

func (a *API) updateTemplateVersion(response http.ResponseWriter, request *http.Request) {
	version, ok := pathVersion(response, request)
	if !ok {
		return
	}
	var input versionInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if strings.TrimSpace(input.Source) == "" || !validSampleData(input.SampleData) {
		writeError(response, http.StatusBadRequest, "invalid_request", "source and a JSON object sampleData are required")
		return
	}
	analysis, err := contract.Analyze(input.Source, input.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	result, err := a.templates.UpdateDraft(request.Context(), request.PathValue("slug"), version, input.Source, analysis.SampleData, analysis.DataSchema, analysis.SchemaHash)
	handleVersionWrite(response, result, err, http.StatusOK)
}

func (a *API) deleteTemplateVersion(response http.ResponseWriter, request *http.Request) {
	version, ok := pathVersion(response, request)
	if !ok {
		return
	}
	err := a.templates.DeleteDraft(request.Context(), request.PathValue("slug"), version)
	if errors.Is(err, templates.ErrConflict) {
		writeError(response, http.StatusConflict, "invalid_transition", "Only draft versions can be discarded")
		return
	}
	if err != nil {
		templateInternalError(response, "delete template draft", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) approveTemplateVersion(response http.ResponseWriter, request *http.Request) {
	version, ok := pathVersion(response, request)
	if !ok {
		return
	}
	draft, err := a.templates.GetVersion(request.Context(), request.PathValue("slug"), version)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Template version was not found")
		return
	}
	if err != nil {
		templateInternalError(response, "get template for approval", err)
		return
	}
	if draft.Status != "draft" {
		writeError(response, http.StatusConflict, "invalid_transition", "Only draft versions can be approved")
		return
	}
	analysis, err := contract.Analyze(draft.Source, draft.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	if hasBlockingDiagnostics(analysis.Diagnostics) {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "contract_analysis_failed", "message": "Template contains report-data access that cannot be analyzed", "details": analysis.Diagnostics}})
		return
	}
	validationErrors, err := contract.Validate(analysis.DataSchema, draft.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	if len(validationErrors) > 0 {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "data_validation_failed", "message": "Sample data does not satisfy the generated contract", "details": validationErrors}})
		return
	}
	if _, err := a.renderer.Render(request.Context(), draft.Source, draft.SampleData); err != nil {
		writeError(response, http.StatusUnprocessableEntity, "compile_failed", "Template must compile before approval: "+err.Error())
		return
	}
	result, err := a.templates.Approve(request.Context(), request.PathValue("slug"), version, draft.Source, draft.SampleData, analysis.DataSchema, analysis.SchemaHash, actor(request))
	handleVersionWrite(response, result, err, http.StatusOK)
}

func (a *API) publishTemplateVersion(response http.ResponseWriter, request *http.Request) {
	versionNumber, ok := pathVersion(response, request)
	if !ok {
		return
	}
	version, err := a.templates.GetVersion(request.Context(), request.PathValue("slug"), versionNumber)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Template version was not found")
		return
	}
	if err != nil {
		templateInternalError(response, "get template for publishing", err)
		return
	}
	if version.Status != "approved" {
		writeError(response, http.StatusConflict, "invalid_transition", "Only approved versions can be published")
		return
	}
	profileID, client, err := a.storage.Default(request.Context())
	if err != nil {
		slog.Error("resolve default storage for template", "slug", version.Slug, "version", version.Version, "error", err)
		writeError(response, http.StatusServiceUnavailable, "storage_unavailable", "Could not configure template storage")
		return
	}
	contents := []byte(version.Source)
	stored, err := client.Put(request.Context(), version.StorageKey, contents)
	if err != nil {
		slog.Error("store published template", "slug", version.Slug, "version", version.Version, "error", err)
		writeError(response, http.StatusServiceUnavailable, "storage_unavailable", "Could not store the published template")
		return
	}
	if err := a.storageMetadata.RecordTemplateArtifact(request.Context(), version.TemplateID, version.Version, profileID, stored, int64(len(contents))); err != nil {
		slog.Error("record published template placement", "slug", version.Slug, "version", version.Version, "profile_id", profileID, "error", err)
		writeError(response, http.StatusInternalServerError, "internal_error", "Could not record the published template")
		return
	}
	result, err := a.templates.Publish(request.Context(), version.Slug, version.Version, actor(request))
	handleVersionWrite(response, result, err, http.StatusOK)
}

func (a *API) previewTemplate(response http.ResponseWriter, request *http.Request) {
	var input versionInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if strings.TrimSpace(input.Source) == "" || !validSampleData(input.SampleData) {
		writeError(response, http.StatusBadRequest, "invalid_request", "source and a JSON object sampleData are required")
		return
	}
	analysis, err := contract.Analyze(input.Source, input.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	validationErrors, err := contract.Validate(analysis.DataSchema, analysis.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	if len(validationErrors) > 0 {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{"code": "data_validation_failed", "message": "Sample data does not satisfy the generated contract", "details": validationErrors}})
		return
	}
	pdf, err := a.renderer.Render(request.Context(), input.Source, analysis.SampleData)
	if err != nil {
		slog.Info("template preview failed", "error", err)
		writeError(response, http.StatusUnprocessableEntity, "compile_failed", err.Error())
		return
	}
	response.Header().Set("Content-Type", "application/pdf")
	response.Header().Set("Content-Disposition", `inline; filename="preview.pdf"`)
	response.WriteHeader(http.StatusOK)
	if _, err := response.Write(pdf); err != nil {
		slog.Error("write preview response", "error", err)
	}
}

func (a *API) analyzeTemplate(response http.ResponseWriter, request *http.Request) {
	var input versionInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if strings.TrimSpace(input.Source) == "" || !validSampleData(input.SampleData) {
		writeError(response, http.StatusBadRequest, "invalid_request", "source and a JSON object sampleData are required")
		return
	}
	result, err := contract.Analyze(input.Source, input.SampleData)
	if err != nil {
		contractError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func hasBlockingDiagnostics(diagnostics []contract.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

func contractError(response http.ResponseWriter, err error) {
	if errors.Is(err, contract.ErrTooComplex) {
		writeError(response, http.StatusRequestEntityTooLarge, "contract_too_complex", err.Error())
		return
	}
	writeError(response, http.StatusBadRequest, "contract_analysis_failed", err.Error())
}

func decodeJSON(response http.ResponseWriter, request *http.Request, value any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "invalid_request", "Request body must contain one JSON object")
		return false
	}
	return true
}

func validSampleData(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var object map[string]any
	return json.Unmarshal(data, &object) == nil && object != nil
}

func pathVersion(response http.ResponseWriter, request *http.Request) (int, bool) {
	version, err := strconv.Atoi(request.PathValue("version"))
	if err != nil || version < 1 {
		writeError(response, http.StatusBadRequest, "invalid_version", "Version must be a positive integer")
		return 0, false
	}
	return version, true
}

func actor(request *http.Request) string {
	user, ok := auth.UserFromContext(request.Context())
	if !ok {
		return "unknown"
	}
	if user.Email != "" {
		return user.Email
	}
	if user.Service {
		return "api-client:" + user.ClientID + ":key:" + user.KeyID
	}
	return user.Subject
}

func handleVersionWrite(response http.ResponseWriter, result templates.Version, err error, successStatus int) {
	if errors.Is(err, templates.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Template was not found")
		return
	}
	if errors.Is(err, templates.ErrConflict) {
		writeError(response, http.StatusConflict, "invalid_transition", "Template version is not in the required state or another draft already exists")
		return
	}
	if err != nil {
		templateInternalError(response, "write template version", err)
		return
	}
	writeJSON(response, successStatus, result)
}

func templateInternalError(response http.ResponseWriter, operation string, err error) {
	slog.Error(operation, "error", err)
	writeError(response, http.StatusInternalServerError, "internal_error", "Template operation failed")
}
