package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesApplication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
		t.Fatal("response does not contain the application root")
	}
}

func TestHandlerFallsBackForClientRoute(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/templates/example", nil)
	response := httptest.NewRecorder()

	Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
}
