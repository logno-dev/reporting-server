package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testDatabaseHealth struct{ err error }

func (health testDatabaseHealth) Ping(context.Context) error { return health.err }

type testQueueHealth struct{ err error }

func (health testQueueHealth) Ping() error { return health.err }

func TestDependencyHealthHandler(t *testing.T) {
	tests := []struct {
		name     string
		database error
		queue    error
		status   int
		body     string
	}{
		{name: "healthy", status: http.StatusOK, body: `"status":"ok"`},
		{name: "database unavailable", database: errors.New("unavailable"), status: http.StatusServiceUnavailable, body: `"component":"database"`},
		{name: "queue unavailable", queue: errors.New("unavailable"), status: http.StatusServiceUnavailable, body: `"component":"queue"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			response := httptest.NewRecorder()
			dependencyHealthHandler(testDatabaseHealth{test.database}, testQueueHealth{test.queue}, "worker").ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if body := response.Body.String(); !strings.Contains(body, test.body) {
				t.Fatalf("body = %q, want %q", body, test.body)
			}
		})
	}
}
