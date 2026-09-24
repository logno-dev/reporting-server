package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"reporting-server/internal/apiclients"
)

type fakeMachineAuthenticator struct {
	identity apiclients.Identity
	err      error
}

func (f fakeMachineAuthenticator) Authenticate(context.Context, string, string) (apiclients.Identity, error) {
	return f.identity, f.err
}

func TestSignedSessionRejectsTampering(t *testing.T) {
	authenticator := &Authenticator{secret: []byte("01234567890123456789012345678901")}
	encoded := authenticator.encode(User{Subject: "user", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	replacement := byte('A')
	if encoded[len(encoded)-1] == replacement {
		replacement = 'B'
	}
	encoded = encoded[:len(encoded)-1] + string(replacement)

	var user User
	if err := authenticator.decode(encoded, &user); err == nil {
		t.Fatal("decode() accepted a tampered session")
	}
}

func TestAdminAuthorization(t *testing.T) {
	authenticator := &Authenticator{secret: []byte("01234567890123456789012345678901")}
	handler := authenticator.RequireAdmin(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))

	viewer := User{Subject: "viewer", Name: "Viewer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	request := httptest.NewRequest(http.MethodPost, "/admin", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: authenticator.encode(viewer)})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403", response.Code)
	}

	admin := User{Subject: "admin", Name: "Admin", Admin: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	request = httptest.NewRequest(http.MethodPost, "/admin", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: authenticator.encode(admin)})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("admin status = %d, want 204", response.Code)
	}
}

func TestServiceKeyScope(t *testing.T) {
	authenticator := &Authenticator{machine: fakeMachineAuthenticator{identity: apiclients.Identity{ClientID: "client", ClientName: "LIMS", KeyID: "key", Scopes: []string{apiclients.ScopeReportsRead}}}}
	handler := authenticator.AllowServiceOrUser(apiclients.ScopeReportsRead, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		user, ok := UserFromContext(request.Context())
		if !ok || !user.Service {
			t.Error("service identity missing from context")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/reports", nil)
	request.Header.Set("Authorization", "Bearer rpt_live_key_secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("service status = %d, want 204", response.Code)
	}
}

func TestServiceKeyMissingScopeIsForbidden(t *testing.T) {
	authenticator := &Authenticator{machine: fakeMachineAuthenticator{identity: apiclients.Identity{Scopes: []string{apiclients.ScopeReportsRead}}}}
	handler := authenticator.AllowServiceOrUser(apiclients.ScopeReportsDownload, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called without required scope")
	}))
	request := httptest.NewRequest(http.MethodGet, "/download", nil)
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
}

func TestInvalidServiceKeyIsUnauthorized(t *testing.T) {
	authenticator := &Authenticator{machine: fakeMachineAuthenticator{err: errors.New("hash mismatch")}}
	handler := authenticator.AllowServiceOrUser(apiclients.ScopeReportsRead, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called with invalid key")
	}))
	request := httptest.NewRequest(http.MethodGet, "/reports", nil)
	request.Header.Set("X-API-Key", "wrong")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
