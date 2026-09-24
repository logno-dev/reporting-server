package apiclients_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"testing"
	"time"

	"reporting-server/internal/apiclients"
	"reporting-server/internal/database"
)

func TestCredentialLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("API_CLIENTS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set API_CLIENTS_TEST_DATABASE_URL to run Postgres credential lifecycle tests")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	service := apiclients.NewService(pool)
	client, err := service.CreateClient(ctx, "integration-"+time.Now().Format("20060102150405.000000000"), "lifecycle test", []string{apiclients.ScopeReportsRead}, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.IssueKey(ctx, client.ID, "original", nil, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.Authenticate(ctx, issued.Secret, "192.0.2.10")
	if err != nil || identity.ClientID != client.ID || identity.KeyID != issued.ID {
		t.Fatalf("Authenticate() = %+v, %v", identity, err)
	}

	keyID, _, err := apiclients.ParseKey(issued.Secret)
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret := make([]byte, 32)
	if _, err := rand.Read(wrongSecret); err != nil {
		t.Fatal(err)
	}
	wrongKey := "rpt_live_" + keyID + "_" + base64.RawURLEncoding.EncodeToString(wrongSecret)
	if _, err := service.Authenticate(ctx, wrongKey, ""); !errors.Is(err, apiclients.ErrInvalidKey) {
		t.Fatalf("hash mismatch error = %v", err)
	}

	replacement, err := service.RotateKey(ctx, client.ID, issued.ID, "replacement", 150*time.Millisecond, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, issued.Secret, ""); err != nil {
		t.Fatalf("old key failed during grace: %v", err)
	}
	if _, err := service.Authenticate(ctx, replacement.Secret, ""); err != nil {
		t.Fatalf("replacement key failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := service.Authenticate(ctx, issued.Secret, ""); !errors.Is(err, apiclients.ErrInvalidKey) {
		t.Fatalf("expired old key error = %v", err)
	}
	if err := service.RevokeKey(ctx, client.ID, replacement.ID, "admin@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, replacement.Secret, ""); !errors.Is(err, apiclients.ErrInvalidKey) {
		t.Fatalf("revoked key error = %v", err)
	}

	expiresAt := time.Now().Add(100 * time.Millisecond)
	expiring, err := service.IssueKey(ctx, client.ID, "short-lived", &expiresAt, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := service.Authenticate(ctx, expiring.Secret, ""); !errors.Is(err, apiclients.ErrInvalidKey) {
		t.Fatalf("expired key error = %v", err)
	}

	third, err := service.IssueKey(ctx, client.ID, "disabled-client-key", nil, "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DisableClient(ctx, client.ID, "admin@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, third.Secret, ""); !errors.Is(err, apiclients.ErrInvalidKey) {
		t.Fatalf("disabled client key error = %v", err)
	}
	keys, err := service.ListKeys(ctx, client.ID)
	if err != nil || len(keys) != 4 {
		t.Fatalf("ListKeys() count = %d, error = %v", len(keys), err)
	}
}
