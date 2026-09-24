package storagecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"reporting-server/internal/storage"
)

// Registry resolves immutable storage profiles and caches the resulting clients.
type Registry struct {
	repository *Repository
	mu         sync.RWMutex
	clients    map[string]storage.Storage
}

func NewRegistry(repository *Repository) *Registry {
	return &Registry{repository: repository, clients: make(map[string]storage.Storage)}
}

// TestProfile builds an uncached client and performs a read-only connectivity test.
func TestProfile(ctx context.Context, input ProfileInput) error {
	client, err := buildClient(input.BackendType, input.PublicConfig, input.Credentials)
	if err != nil {
		return err
	}
	tester, ok := client.(interface{ Test(context.Context) error })
	if !ok {
		return fmt.Errorf("storage backend does not support connectivity testing")
	}
	return tester.Test(ctx)
}

func (r *Registry) Test(ctx context.Context, profileID string) error {
	client, err := r.Resolve(ctx, profileID)
	if err != nil {
		return err
	}
	tester, ok := client.(interface{ Test(context.Context) error })
	if !ok {
		return fmt.Errorf("storage backend does not support connectivity testing")
	}
	return tester.Test(ctx)
}

func (r *Registry) Resolve(ctx context.Context, profileID string) (storage.Storage, error) {
	r.mu.RLock()
	client := r.clients[profileID]
	r.mu.RUnlock()
	if client != nil {
		return client, nil
	}

	profile, err := r.repository.GetProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if profile.State != "active" {
		return nil, fmt.Errorf("storage profile %s is not active", profileID)
	}
	credentials, err := r.repository.ProfileCredentials(ctx, profileID)
	if err != nil {
		return nil, err
	}
	client, err = buildClient(profile.BackendType, profile.PublicConfig, credentials)
	if err != nil {
		return nil, fmt.Errorf("configure storage profile %s: %w", profileID, err)
	}

	r.mu.Lock()
	if existing := r.clients[profileID]; existing != nil {
		client = existing
	} else {
		r.clients[profileID] = client
	}
	r.mu.Unlock()
	return client, nil
}

type filesystemConfig struct {
	Root string `json:"root"`
}

type s3Config struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	UseSSL   *bool  `json:"useSSL"`
}

type s3Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

func buildClient(backend string, publicConfig, credentials json.RawMessage) (storage.Storage, error) {
	switch backend {
	case "filesystem":
		var config filesystemConfig
		if err := decodeStrict(publicConfig, &config); err != nil {
			return nil, fmt.Errorf("invalid filesystem public config: %w", err)
		}
		if strings.TrimSpace(config.Root) == "" {
			return nil, fmt.Errorf("filesystem root is required")
		}
		if len(credentials) != 0 {
			var empty struct{}
			if err := decodeStrict(credentials, &empty); err != nil {
				return nil, fmt.Errorf("filesystem credentials must be an empty object")
			}
		}
		return storage.NewFilesystem(config.Root), nil
	case "s3":
		var config s3Config
		if err := decodeStrict(publicConfig, &config); err != nil {
			return nil, fmt.Errorf("invalid S3 public config: %w", err)
		}
		var secret s3Credentials
		if err := decodeStrict(credentials, &secret); err != nil {
			return nil, fmt.Errorf("invalid S3 credentials")
		}
		if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.Region) == "" || config.UseSSL == nil {
			return nil, fmt.Errorf("S3 endpoint, bucket, region, and useSSL are required")
		}
		if strings.TrimSpace(secret.AccessKeyID) == "" || secret.SecretAccessKey == "" {
			return nil, fmt.Errorf("S3 access key ID and secret access key are required")
		}
		return storage.NewS3(config.Endpoint, config.Bucket, config.Region, secret.AccessKeyID, secret.SecretAccessKey, *config.UseSSL)
	default:
		return nil, fmt.Errorf("unsupported backend type %q", backend)
	}
}

func decodeStrict(data []byte, destination any) error {
	if len(data) == 0 {
		return fmt.Errorf("JSON object is required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return fmt.Errorf("must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("must contain one JSON object")
	}
	return nil
}
