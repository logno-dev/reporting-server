package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"reporting-server/internal/api"
	"reporting-server/internal/apiclients"
	"reporting-server/internal/auth"
	"reporting-server/internal/config"
	"reporting-server/internal/database"
	"reporting-server/internal/jobs"
	reportqueue "reporting-server/internal/queue"
	"reporting-server/internal/renderer"
	"reporting-server/internal/storage"
	"reporting-server/internal/storagecontrol"
	"reporting-server/internal/templates"
	"reporting-server/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return err
	}

	reports := jobs.NewRepository(pool)
	templateRepository := templates.NewRepository(pool)
	credentialCipher, err := storagecontrol.NewCredentialCipher(cfg.StorageMasterKey)
	if err != nil {
		return err
	}
	storageRepository := storagecontrol.NewRepository(pool, credentialCipher)
	legacyProfile, err := legacyStorageProfile(cfg)
	if err != nil {
		return err
	}
	if err := storageRepository.BootstrapLegacy(ctx, legacyProfile); err != nil {
		return err
	}
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddress, Password: cfg.RedisPassword, DB: cfg.RedisDB}
	store, err := buildStorage(cfg)
	if err != nil {
		return err
	}
	if err := syncLegacyPublishedTemplates(ctx, templateRepository, store); err != nil {
		return err
	}
	storageRegistry := storagecontrol.NewRegistry(storageRepository)
	storageService := storagecontrol.NewService(storageRepository, storageRegistry, store)
	render := renderer.New(cfg.TypstBinary, cfg.TypstRoot, cfg.RenderTimeout)
	if cfg.Role == "worker" {
		healthQueue := reportqueue.NewClient(redis, cfg.RenderTimeout+30*time.Second)
		defer healthQueue.Close()
		healthServer := &http.Server{
			Addr:              ":8081",
			Handler:           dependencyHealthHandler(pool, healthQueue, "worker"),
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      3 * time.Second,
		}
		go func() {
			if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("worker health server stopped", "error", err)
			}
		}()
		worker := reportqueue.NewWorker(redis, cfg.WorkerConcurrency, reports, render, storageService, storageRepository, cfg.RendererVersion, cfg.RenderTimeout+30*time.Second)
		go func() {
			<-ctx.Done()
			worker.Shutdown()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := healthServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("worker health shutdown", "error", err)
			}
		}()
		slog.Info("worker starting", "concurrency", cfg.WorkerConcurrency)
		return worker.Run()
	}

	queueClient := reportqueue.NewClient(redis, cfg.RenderTimeout+30*time.Second)
	defer queueClient.Close()
	dispatcher := reportqueue.NewDispatcher(queueClient, reports, storageRepository)
	go dispatcher.Run(ctx)
	apiClients := apiclients.NewService(pool)
	authenticator, err := auth.New(ctx, auth.Config{
		Development: cfg.Environment == "development",
		IssuerURL:   cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret,
		RedirectURL: cfg.OIDCRedirectURL, AdminGroup: cfg.OIDCAdminGroup,
		SessionSecret: cfg.SessionSecret,
	}, apiClients)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           api.New(reports, templateRepository, queueClient, render, storageService, storageRepository, apiClients, pool, authenticator, web.Handler()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RenderTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP shutdown", "error", err)
		}
	}()
	slog.Info("API starting", "address", cfg.HTTPAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type databaseHealthChecker interface {
	Ping(context.Context) error
}

type queueHealthChecker interface {
	Ping() error
}

func dependencyHealthHandler(database databaseHealthChecker, queue queueHealthChecker, role string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		response.Header().Set("Content-Type", "application/json")
		if err := database.Ping(ctx); err != nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]string{"status": "unhealthy", "role": role, "component": "database"})
			return
		}
		if err := queue.Ping(); err != nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]string{"status": "unhealthy", "role": role, "component": "queue"})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"status": "ok", "role": role})
	})
}

func legacyStorageProfile(cfg config.Config) (storagecontrol.ProfileInput, error) {
	profile := storagecontrol.ProfileInput{
		Name: "deployment-local-storage", BackendType: "filesystem", State: "active",
	}
	encoded, err := json.Marshal(map[string]any{"root": cfg.ReportsDirectory})
	if err != nil {
		return storagecontrol.ProfileInput{}, err
	}
	profile.PublicConfig = encoded
	return profile, nil
}

func buildStorage(cfg config.Config) (storage.Storage, error) {
	return storage.NewFilesystem(cfg.ReportsDirectory), nil
}

// syncLegacyPublishedTemplates preserves startup behavior for artifacts that predate placement-aware writes.
func syncLegacyPublishedTemplates(ctx context.Context, repository *templates.Repository, store storage.Storage) error {
	published, err := repository.PublishedArtifacts(ctx, storagecontrol.LegacyProfileID)
	if err != nil {
		return err
	}
	for _, template := range published {
		if _, err := store.Put(ctx, template.StorageKey, []byte(template.Source)); err != nil {
			return err
		}
	}
	slog.Info("published templates synchronized", "count", len(published))
	return nil
}
