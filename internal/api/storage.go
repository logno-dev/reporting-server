package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"reporting-server/internal/storagecontrol"
)

var validProfileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{1,62}[A-Za-z0-9]$`)

type profileRequest struct {
	Name                string          `json:"name"`
	BackendType         string          `json:"backendType"`
	PublicConfig        json.RawMessage `json:"publicConfig"`
	Credentials         json.RawMessage `json:"credentials"`
	RequestRateLimit    *int            `json:"requestRateLimit"`
	TransferConcurrency *int            `json:"transferConcurrency"`
}

func decodeOne(response http.ResponseWriter, request *http.Request, value any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "invalid_request", "Request body must contain one JSON object")
		return false
	}
	return true
}

func (a *API) listStorageProfiles(response http.ResponseWriter, request *http.Request) {
	profiles, err := a.storageMetadata.ListProfiles(request.Context())
	if err != nil {
		writeError(response, 500, "internal_error", "Could not list storage profiles")
		return
	}
	writeJSON(response, 200, profiles)
}

func profileInput(request profileRequest) (storagecontrol.ProfileInput, error) {
	request.Name = strings.TrimSpace(request.Name)
	if !validProfileName.MatchString(request.Name) {
		return storagecontrol.ProfileInput{}, errors.New("name must be 3-64 characters using letters, numbers, spaces, dot, underscore, or hyphen")
	}
	if request.BackendType != "s3" {
		return storagecontrol.ProfileInput{}, errors.New("only S3-compatible profiles can be created; the legacy filesystem profile is managed by configuration")
	}
	if request.RequestRateLimit != nil && (*request.RequestRateLimit < 1 || *request.RequestRateLimit > 10000) {
		return storagecontrol.ProfileInput{}, errors.New("requestRateLimit must be between 1 and 10000")
	}
	if request.TransferConcurrency != nil && (*request.TransferConcurrency < 1 || *request.TransferConcurrency > 1000) {
		return storagecontrol.ProfileInput{}, errors.New("transferConcurrency must be between 1 and 1000")
	}
	input := storagecontrol.ProfileInput{Name: request.Name, BackendType: "s3", State: "active", PublicConfig: request.PublicConfig, Credentials: request.Credentials, RequestRateLimit: request.RequestRateLimit, TransferConcurrency: request.TransferConcurrency}
	return input, nil
}

func (a *API) createStorageProfile(response http.ResponseWriter, request *http.Request) {
	var body profileRequest
	if !decodeOne(response, request, &body) {
		return
	}
	input, err := profileInput(body)
	if err != nil {
		writeError(response, 400, "invalid_request", err.Error())
		return
	}
	if err := storagecontrol.TestProfile(request.Context(), input); err != nil {
		writeError(response, 422, "connectivity_failed", err.Error())
		return
	}
	profile, err := a.storageMetadata.CreateProfile(request.Context(), input)
	if err != nil {
		writeError(response, 409, "profile_not_created", "Could not create storage profile: "+err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, profile)
}

func (a *API) testStorageProfile(response http.ResponseWriter, request *http.Request) {
	if err := a.storage.Test(request.Context(), request.PathValue("id")); err != nil {
		writeError(response, 422, "connectivity_failed", err.Error())
		return
	}
	writeJSON(response, 200, map[string]bool{"ok": true})
}

func (a *API) getStorageDefault(response http.ResponseWriter, request *http.Request) {
	profile, err := a.storageMetadata.DefaultProfile(request.Context())
	if err != nil {
		writeError(response, 404, "not_found", "Default profile was not found")
		return
	}
	writeJSON(response, 200, profile)
}
func (a *API) setStorageDefault(response http.ResponseWriter, request *http.Request) {
	var body struct {
		ProfileID string `json:"profileId"`
	}
	if !decodeOne(response, request, &body) {
		return
	}
	profile, err := a.storageMetadata.GetProfile(request.Context(), body.ProfileID)
	if err != nil || profile.State != "active" {
		writeError(response, 422, "invalid_profile", "Active profile was not found")
		return
	}
	if err = a.storageMetadata.SetDefaultProfile(request.Context(), body.ProfileID); err != nil {
		writeError(response, 500, "internal_error", "Could not set default profile")
		return
	}
	writeJSON(response, 200, profile)
}

func (a *API) listStorageMigrations(response http.ResponseWriter, request *http.Request) {
	items, err := a.storageMetadata.ListMigrations(request.Context())
	if err != nil {
		writeError(response, 500, "internal_error", "Could not list migrations")
		return
	}
	writeJSON(response, 200, items)
}
func (a *API) createStorageMigration(response http.ResponseWriter, request *http.Request) {
	var body struct {
		SourceProfileID      string `json:"sourceProfileId"`
		DestinationProfileID string `json:"destinationProfileId"`
	}
	if !decodeOne(response, request, &body) {
		return
	}
	if body.SourceProfileID == body.DestinationProfileID || body.SourceProfileID == "" {
		writeError(response, 400, "invalid_request", "Distinct source and destination profiles are required")
		return
	}
	for _, id := range []string{body.SourceProfileID, body.DestinationProfileID} {
		p, err := a.storageMetadata.GetProfile(request.Context(), id)
		if err != nil || p.State != "active" {
			writeError(response, 422, "invalid_profile", "Both profiles must be active")
			return
		}
	}
	migration, err := a.storageMetadata.CreateMigration(request.Context(), body.SourceProfileID, body.DestinationProfileID, nil)
	if err != nil {
		writeError(response, 500, "internal_error", "Could not create migration")
		return
	}
	writeJSON(response, 201, migration)
}
func (a *API) getStorageMigration(response http.ResponseWriter, request *http.Request) {
	migration, items, err := a.storageMetadata.GetMigration(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(response, 404, "not_found", "Migration was not found")
		return
	}
	writeJSON(response, 200, map[string]any{"migration": migration, "items": items})
}
func (a *API) storageMigrationAction(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	var err error
	switch request.PathValue("action") {
	case "resume":
		err = a.storageMetadata.ResumeMigration(request.Context(), id)
	case "pause":
		err = a.storageMetadata.PauseMigration(request.Context(), id)
	case "cancel":
		err = a.storageMetadata.CancelMigration(request.Context(), id)
	default:
		writeError(response, 404, "not_found", "Action was not found")
		return
	}
	if err != nil {
		writeError(response, 409, "invalid_state", "Migration cannot perform that action from its current state")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
