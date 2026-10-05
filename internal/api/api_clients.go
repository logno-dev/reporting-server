package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"reporting-server/internal/apiclients"
)

type apiClientInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
}

type apiKeyInput struct {
	Label         string          `json:"label"`
	ExpiresAt     *time.Time      `json:"expiresAt"`
	TemplateSlugs json.RawMessage `json:"templateSlugs"`
}

type rotateAPIKeyInput struct {
	Label         string          `json:"label"`
	GraceSeconds  int64           `json:"graceSeconds"`
	TemplateSlugs json.RawMessage `json:"templateSlugs"`
}

type apiKeyTemplateAccessInput struct {
	TemplateSlugs json.RawMessage `json:"templateSlugs"`
}

func (a *API) listAPIClients(response http.ResponseWriter, request *http.Request) {
	clients, err := a.apiClients.ListClients(request.Context())
	if err != nil {
		apiClientError(response, "list API clients", err)
		return
	}
	writeJSON(response, http.StatusOK, clients)
}

func (a *API) createAPIClient(response http.ResponseWriter, request *http.Request) {
	var input apiClientInput
	if !decodeJSON(response, request, &input) {
		return
	}
	client, err := a.apiClients.CreateClient(request.Context(), input.Name, input.Description, input.Scopes, actor(request))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "Client name and valid scopes are required")
		return
	}
	writeJSON(response, http.StatusCreated, client)
}

func (a *API) getAPIClient(response http.ResponseWriter, request *http.Request) {
	client, err := a.apiClients.GetClient(request.Context(), request.PathValue("id"))
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "API client was not found")
		return
	}
	if err != nil {
		apiClientError(response, "get API client", err)
		return
	}
	writeJSON(response, http.StatusOK, client)
}

func (a *API) disableAPIClient(response http.ResponseWriter, request *http.Request) {
	if err := requireEmptyJSON(response, request); err != nil {
		return
	}
	err := a.apiClients.DisableClient(request.Context(), request.PathValue("id"), actor(request))
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Enabled API client was not found")
		return
	}
	if err != nil {
		apiClientError(response, "disable API client", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) listAPIKeys(response http.ResponseWriter, request *http.Request) {
	keys, err := a.apiClients.ListKeys(request.Context(), request.PathValue("id"))
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "API client was not found")
		return
	}
	if err != nil {
		apiClientError(response, "list API keys", err)
		return
	}
	writeJSON(response, http.StatusOK, keys)
}

func (a *API) issueAPIKey(response http.ResponseWriter, request *http.Request) {
	var input apiKeyInput
	if !decodeJSON(response, request, &input) {
		return
	}
	templateSlugs, _, err := parseTemplateSelection(input.TemplateSlugs)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "templateSlugs must be null or an array of template slugs")
		return
	}
	issued, err := a.apiClients.IssueKey(request.Context(), request.PathValue("id"), input.Label, input.ExpiresAt, templateSlugs, actor(request))
	writeIssuedKey(response, issued, err)
}

func (a *API) rotateAPIKey(response http.ResponseWriter, request *http.Request) {
	var input rotateAPIKeyInput
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.GraceSeconds < 0 || input.GraceSeconds > int64((30*24*time.Hour)/time.Second) {
		writeError(response, http.StatusBadRequest, "invalid_request", "graceSeconds must be between 0 and 2592000")
		return
	}
	templateSlugs, supplied, err := parseTemplateSelection(input.TemplateSlugs)
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "templateSlugs must be null or an array of template slugs")
		return
	}
	var replacementTemplateSlugs *[]string
	if supplied {
		replacementTemplateSlugs = &templateSlugs
	}
	issued, err := a.apiClients.RotateKey(request.Context(), request.PathValue("id"), request.PathValue("keyId"), input.Label, time.Duration(input.GraceSeconds)*time.Second, replacementTemplateSlugs, actor(request))
	writeIssuedKey(response, issued, err)
}

func parseTemplateSelection(raw json.RawMessage) ([]string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var templateSlugs []string
	if err := json.Unmarshal(raw, &templateSlugs); err != nil || templateSlugs == nil {
		return nil, true, errors.New("invalid template selection")
	}
	return templateSlugs, true, nil
}

func (a *API) updateAPIKeyTemplateAccess(response http.ResponseWriter, request *http.Request) {
	var input apiKeyTemplateAccessInput
	if !decodeJSON(response, request, &input) {
		return
	}
	templateSlugs, supplied, err := parseTemplateSelection(input.TemplateSlugs)
	if err != nil || !supplied {
		writeError(response, http.StatusBadRequest, "invalid_request", "templateSlugs must be null or an array of template slugs")
		return
	}
	err = a.apiClients.UpdateKeyTemplateAccess(request.Context(), request.PathValue("id"), request.PathValue("keyId"), templateSlugs, actor(request))
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Active API key was not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "Template selection is invalid")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) revokeAPIKey(response http.ResponseWriter, request *http.Request) {
	if err := requireEmptyJSON(response, request); err != nil {
		return
	}
	err := a.apiClients.RevokeKey(request.Context(), request.PathValue("id"), request.PathValue("keyId"), actor(request))
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "Active API key was not found")
		return
	}
	if err != nil {
		apiClientError(response, "revoke API key", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func writeIssuedKey(response http.ResponseWriter, issued apiclients.IssuedKey, err error) {
	if errors.Is(err, apiclients.ErrNotFound) {
		writeError(response, http.StatusNotFound, "not_found", "API client or key was not found")
		return
	}
	if errors.Is(err, apiclients.ErrConflict) {
		writeError(response, http.StatusConflict, "invalid_state", "API client or key is not active")
		return
	}
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "Key label, expiry, rotation grace, or template selection is invalid")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusCreated, issued)
}

func requireEmptyJSON(response http.ResponseWriter, request *http.Request) error {
	if request.Body == nil || request.ContentLength == 0 {
		return nil
	}
	var input struct{}
	if !decodeJSON(response, request, &input) {
		return errors.New("invalid request")
	}
	return nil
}

func apiClientError(response http.ResponseWriter, operation string, err error) {
	slog.Error(operation, "error", err)
	writeError(response, http.StatusInternalServerError, "internal_error", "API credential operation failed")
}
