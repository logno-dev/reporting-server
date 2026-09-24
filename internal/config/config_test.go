package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesDeterministicDevelopmentStorageMasterKey(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MASTER_KEY", "")

	first, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	second, err := Load()
	if err != nil {
		t.Fatalf("Load() second error = %v", err)
	}
	if len(first.StorageMasterKey) != 32 || !bytes.Equal(first.StorageMasterKey, second.StorageMasterKey) {
		t.Fatal("development storage master key is not a deterministic 32-byte key")
	}
}

func TestLoadReadsStorageMasterKey(t *testing.T) {
	want := bytes.Repeat([]byte{0x5a}, 32)
	t.Setenv("STORAGE_MASTER_KEY", base64.StdEncoding.EncodeToString(want))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !bytes.Equal(cfg.StorageMasterKey, want) {
		t.Fatal("StorageMasterKey does not match decoded environment value")
	}
}

func TestLoadRejectsInvalidStorageMasterKey(t *testing.T) {
	for _, value := range []string{"not-base64", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("STORAGE_MASTER_KEY", value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() accepted an invalid storage master key")
			}
		})
	}
}

func TestLoadRequiresStorageMasterKeyForProductionWorker(t *testing.T) {
	t.Setenv("APP_ROLE", "worker")
	t.Setenv("APP_ENV", "production")
	t.Setenv("STORAGE_MASTER_KEY", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "STORAGE_MASTER_KEY") {
		t.Fatal("Load() accepted a production worker without STORAGE_MASTER_KEY")
	}
}

func TestLoadRequiresStorageMasterKeyForProductionAPI(t *testing.T) {
	t.Setenv("APP_ROLE", "api")
	t.Setenv("APP_ENV", "production")
	t.Setenv("STORAGE_MASTER_KEY", "")
	t.Setenv("OIDC_ISSUER_URL", "https://issuer.example")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URL", "https://reports.example/callback")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "STORAGE_MASTER_KEY") {
		t.Fatal("Load() accepted a production API without STORAGE_MASTER_KEY")
	}
}

func TestLoadRejectsInvalidRole(t *testing.T) {
	t.Setenv("APP_ROLE", "scheduler")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an invalid role")
	}
}

func TestLoadReadsWorkerSettings(t *testing.T) {
	t.Setenv("APP_ROLE", "worker")
	t.Setenv("WORKER_CONCURRENCY", "7")
	t.Setenv("RENDER_TIMEOUT", "90s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WorkerConcurrency != 7 {
		t.Errorf("WorkerConcurrency = %d, want 7", cfg.WorkerConcurrency)
	}
	if cfg.RenderTimeout != 90*time.Second {
		t.Errorf("RenderTimeout = %s, want 90s", cfg.RenderTimeout)
	}
}

func TestLoadRequiresOIDCSettingsInProduction(t *testing.T) {
	t.Setenv("APP_ROLE", "api")
	t.Setenv("APP_ENV", "production")
	t.Setenv("STORAGE_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("OIDC_ISSUER_URL", "")
	t.Setenv("OIDC_CLIENT_ID", "")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("OIDC_REDIRECT_URL", "")
	t.Setenv("SESSION_SECRET", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted production without OIDC settings")
	}
}
