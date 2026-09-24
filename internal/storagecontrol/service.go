package storagecontrol

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"reporting-server/internal/storage"
)

var ErrIntegrity = errors.New("storage object failed integrity verification")

// Service performs normal runtime reads and writes through profile metadata.
type Service struct {
	repository *Repository
	registry   *Registry
	legacy     storage.Storage
}

func NewService(repository *Repository, registry *Registry, legacy storage.Storage) *Service {
	return &Service{repository: repository, registry: registry, legacy: legacy}
}

func (s *Service) Default(ctx context.Context) (string, storage.Storage, error) {
	profile, err := s.repository.DefaultProfile(ctx)
	if err != nil {
		return "", nil, err
	}
	client, err := s.registry.Resolve(ctx, profile.ID)
	return profile.ID, client, err
}

func (s *Service) Resolve(ctx context.Context, profileID string) (storage.Storage, error) {
	return s.registry.Resolve(ctx, profileID)
}

func (s *Service) Test(ctx context.Context, profileID string) error {
	return s.registry.Test(ctx, profileID)
}

func (s *Service) WriteTarget(ctx context.Context, profileID *string) (string, storage.Storage, error) {
	if profileID != nil {
		client, err := s.registry.Resolve(ctx, *profileID)
		return *profileID, client, err
	}
	if s.legacy == nil {
		return "", nil, ErrNotFound
	}
	return LegacyProfileID, s.legacy, nil
}

func (s *Service) ReadReport(ctx context.Context, jobID, preferredProfileID, legacyKey, expectedSHA256 string) ([]byte, error) {
	placements, err := s.repository.ReportPlacements(ctx, jobID, preferredProfileID)
	if err != nil {
		return nil, err
	}
	return s.read(ctx, placements, legacyKey, expectedSHA256)
}

func (s *Service) ReadTemplate(ctx context.Context, templateID int64, version int, preferredProfileID, legacyKey string) ([]byte, error) {
	placements, err := s.repository.TemplatePlacements(ctx, templateID, version, preferredProfileID)
	if err != nil {
		return nil, err
	}
	return s.read(ctx, placements, legacyKey, "")
}

func (s *Service) read(ctx context.Context, placements []Placement, legacyKey, expectedSHA256 string) ([]byte, error) {
	if len(placements) == 0 {
		if s.legacy == nil || legacyKey == "" {
			return nil, ErrNotFound
		}
		contents, err := s.legacy.Get(ctx, legacyKey)
		if err != nil {
			return nil, err
		}
		if !matchesSHA256(contents, expectedSHA256) {
			return nil, ErrIntegrity
		}
		return contents, nil
	}

	var lastErr error
	for _, placement := range placements {
		client, err := s.registry.Resolve(ctx, placement.ProfileID)
		if err != nil {
			lastErr = err
			continue
		}
		contents, err := client.Get(ctx, placement.StorageKey)
		if err != nil {
			lastErr = err
			continue
		}
		digest := expectedSHA256
		if placement.SHA256 != nil {
			digest = *placement.SHA256
		}
		if !matchesSHA256(contents, digest) || !matchesSHA256(contents, expectedSHA256) {
			lastErr = ErrIntegrity
			continue
		}
		return contents, nil
	}
	if lastErr == nil {
		lastErr = ErrNotFound
	}
	return nil, fmt.Errorf("read available storage placements: %w", lastErr)
}

func matchesSHA256(contents []byte, expected string) bool {
	if expected == "" {
		return true
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents)) == expected
}
