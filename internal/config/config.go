package config

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Role              string
	Environment       string
	HTTPAddress       string
	DatabaseURL       string
	RedisAddress      string
	RedisPassword     string
	RedisDB           int
	OIDCIssuerURL     string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	OIDCAdminGroup    string
	SessionSecret     string
	TypstBinary       string
	TypstRoot         string
	ReportsDirectory  string
	StorageMasterKey  []byte
	RendererVersion   string
	WorkerConcurrency int
	RenderTimeout     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Role:              env("APP_ROLE", "api"),
		Environment:       env("APP_ENV", "development"),
		HTTPAddress:       env("HTTP_ADDRESS", ":8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://reporting:reporting@localhost:5432/reporting?sslmode=disable"),
		RedisAddress:      env("REDIS_ADDRESS", "localhost:6379"),
		RedisPassword:     os.Getenv("REDIS_PASSWORD"),
		OIDCIssuerURL:     strings.TrimSpace(os.Getenv("OIDC_ISSUER_URL")),
		OIDCClientID:      strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")),
		OIDCClientSecret:  os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:   strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URL")),
		OIDCAdminGroup:    env("OIDC_ADMIN_GROUP", "report-admins"),
		SessionSecret:     os.Getenv("SESSION_SECRET"),
		TypstBinary:       env("TYPST_BINARY", "typst"),
		TypstRoot:         env("TYPST_ROOT", "typst"),
		ReportsDirectory:  env("REPORTS_DIRECTORY", "data/reports"),
		RendererVersion:   env("RENDERER_VERSION", "dev"),
		WorkerConcurrency: runtime.NumCPU(),
		RenderTimeout:     60 * time.Second,
	}

	var err error
	if cfg.RedisDB, err = envInt("REDIS_DB", 0); err != nil {
		return Config{}, err
	}
	if cfg.WorkerConcurrency, err = envInt("WORKER_CONCURRENCY", cfg.WorkerConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.RenderTimeout, err = envDuration("RENDER_TIMEOUT", cfg.RenderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.StorageMasterKey, err = storageMasterKey(cfg.Environment); err != nil {
		return Config{}, err
	}

	if cfg.Role != "api" && cfg.Role != "worker" {
		return Config{}, fmt.Errorf("APP_ROLE must be api or worker, got %q", cfg.Role)
	}
	if cfg.Environment != "development" && cfg.Environment != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be development or production, got %q", cfg.Environment)
	}
	if cfg.Role == "api" && cfg.Environment == "production" && (cfg.OIDCIssuerURL == "" || cfg.OIDCClientID == "" || cfg.OIDCClientSecret == "" || cfg.OIDCRedirectURL == "" || len(cfg.SessionSecret) < 32) {
		return Config{}, fmt.Errorf("OIDC_ISSUER_URL, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, OIDC_REDIRECT_URL, and a 32-character SESSION_SECRET are required when APP_ENV=production")
	}
	if cfg.WorkerConcurrency < 1 {
		return Config{}, fmt.Errorf("WORKER_CONCURRENCY must be positive")
	}
	return cfg, nil
}

func storageMasterKey(environment string) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv("STORAGE_MASTER_KEY"))
	if raw == "" {
		if environment == "development" {
			// This stable fallback is intentionally unsafe outside local development.
			digest := sha256.Sum256([]byte("reporting-server development-only storage master key"))
			return digest[:], nil
		}
		return nil, fmt.Errorf("STORAGE_MASTER_KEY is required when APP_ENV=production")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("STORAGE_MASTER_KEY must be valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("STORAGE_MASTER_KEY must decode to exactly 32 bytes")
	}
	return key, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}
