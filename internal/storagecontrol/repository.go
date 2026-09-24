package storagecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"reporting-server/internal/storage"
)

const LegacyProfileID = "00000000000000000000000000"

var ErrNotFound = errors.New("storage control-plane record not found")

type Profile struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	BackendType          string          `json:"backendType"`
	State                string          `json:"state"`
	PublicConfig         json.RawMessage `json:"publicConfig"`
	CredentialConfigured bool            `json:"credentialConfigured"`
	RequestRateLimit     *int            `json:"requestRateLimit,omitempty"`
	TransferConcurrency  *int            `json:"transferConcurrency,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
}

type ProfileInput struct {
	Name                string
	BackendType         string
	State               string
	PublicConfig        json.RawMessage
	Credentials         json.RawMessage
	RequestRateLimit    *int
	TransferConcurrency *int
}

type Object struct {
	ID              string      `json:"id"`
	ObjectType      string      `json:"objectType"`
	LogicalKey      string      `json:"logicalKey"`
	ReportJobID     *string     `json:"reportJobId,omitempty"`
	TemplateID      *int64      `json:"templateId,omitempty"`
	TemplateVersion *int        `json:"templateVersion,omitempty"`
	SHA256          *string     `json:"sha256,omitempty"`
	SizeBytes       *int64      `json:"sizeBytes,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	Placements      []Placement `json:"placements,omitempty"`
}

type ObjectInput struct {
	ObjectType      string
	LogicalKey      string
	ReportJobID     *string
	TemplateID      *int64
	TemplateVersion *int
	SHA256          *string
	SizeBytes       *int64
}

type Placement struct {
	ID         string     `json:"id"`
	ObjectID   string     `json:"objectId"`
	ProfileID  string     `json:"profileId"`
	StorageKey string     `json:"storageKey"`
	State      string     `json:"state"`
	SHA256     *string    `json:"sha256,omitempty"`
	SizeBytes  *int64     `json:"sizeBytes,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	Error      *string    `json:"error,omitempty"`
}

type Migration struct {
	ID                   string     `json:"id"`
	SourceProfileID      string     `json:"sourceProfileId"`
	DestinationProfileID string     `json:"destinationProfileId"`
	State                string     `json:"state"`
	CreatedAt            time.Time  `json:"createdAt"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	CompletedAt          *time.Time `json:"completedAt,omitempty"`
	Error                *string    `json:"error,omitempty"`
	TotalItems           int        `json:"totalItems"`
	CompletedItems       int        `json:"completedItems"`
	FailedItems          int        `json:"failedItems"`
}

type MigrationItem struct {
	MigrationID string     `json:"migrationId"`
	ObjectID    string     `json:"objectId"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Error       *string    `json:"error,omitempty"`
	StorageKey  string     `json:"storageKey"`
}

type Repository struct {
	pool   *pgxpool.Pool
	cipher *CredentialCipher
}

func NewRepository(pool *pgxpool.Pool, cipher *CredentialCipher) *Repository {
	return &Repository{pool: pool, cipher: cipher}
}

func (r *Repository) CreateProfile(ctx context.Context, input ProfileInput) (Profile, error) {
	id := ulid.Make().String()
	encrypted, err := r.encryptCredentials(id, input.Credentials)
	if err != nil {
		return Profile{}, err
	}
	config := input.PublicConfig
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	const query = `
		INSERT INTO storage_profiles
			(id, name, backend_type, state, public_config, encrypted_credentials, request_rate_limit, transfer_concurrency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, name, backend_type, state, public_config, encrypted_credentials IS NOT NULL,
		          request_rate_limit, transfer_concurrency, created_at`
	profile, err := scanProfile(r.pool.QueryRow(ctx, query, id, input.Name, input.BackendType, input.State, config, encrypted, input.RequestRateLimit, input.TransferConcurrency))
	if err != nil {
		return Profile{}, fmt.Errorf("create storage profile: %w", err)
	}
	return profile, nil
}

func (r *Repository) GetProfile(ctx context.Context, id string) (Profile, error) {
	const query = `SELECT id, name, backend_type, state, public_config, encrypted_credentials IS NOT NULL, request_rate_limit, transfer_concurrency, created_at FROM storage_profiles WHERE id = $1`
	profile, err := scanProfile(r.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get storage profile: %w", err)
	}
	return profile, nil
}

func (r *Repository) ListProfiles(ctx context.Context) ([]Profile, error) {
	const query = `SELECT id, name, backend_type, state, public_config, encrypted_credentials IS NOT NULL, request_rate_limit, transfer_concurrency, created_at FROM storage_profiles ORDER BY created_at, id`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list storage profiles: %w", err)
	}
	defer rows.Close()
	var profiles []Profile
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan storage profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// ProfileCredentials is deliberately separate from the public Profile model.
func (r *Repository) ProfileCredentials(ctx context.Context, id string) (json.RawMessage, error) {
	var encrypted []byte
	if err := r.pool.QueryRow(ctx, `SELECT encrypted_credentials FROM storage_profiles WHERE id = $1`, id).Scan(&encrypted); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get profile credentials: %w", err)
	}
	if encrypted == nil {
		return nil, nil
	}
	plaintext, err := r.cipher.Decrypt(encrypted, []byte(id))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(plaintext), nil
}

func (r *Repository) SetDefaultProfile(ctx context.Context, profileID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO storage_defaults (singleton, profile_id) VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET profile_id = EXCLUDED.profile_id, updated_at = now()`, profileID)
	if err != nil {
		return fmt.Errorf("set default storage profile: %w", err)
	}
	return nil
}

func (r *Repository) DefaultProfile(ctx context.Context) (Profile, error) {
	const query = `
		SELECT p.id, p.name, p.backend_type, p.state, p.public_config, p.encrypted_credentials IS NOT NULL,
		       p.request_rate_limit, p.transfer_concurrency, p.created_at
		FROM storage_defaults d JOIN storage_profiles p ON p.id = d.profile_id WHERE d.singleton`
	profile, err := scanProfile(r.pool.QueryRow(ctx, query))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get default storage profile: %w", err)
	}
	return profile, nil
}

func (r *Repository) CreateObject(ctx context.Context, input ObjectInput) (Object, error) {
	id := ulid.Make().String()
	const query = `
		INSERT INTO storage_objects (id, object_type, logical_key, report_job_id, template_id, template_version, sha256, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, object_type, logical_key, report_job_id, template_id, template_version, sha256, size_bytes, created_at`
	object, err := scanObject(r.pool.QueryRow(ctx, query, id, input.ObjectType, input.LogicalKey, input.ReportJobID, input.TemplateID, input.TemplateVersion, input.SHA256, input.SizeBytes))
	if err != nil {
		return Object{}, fmt.Errorf("create storage object: %w", err)
	}
	return object, nil
}

func (r *Repository) GetObject(ctx context.Context, id string) (Object, error) {
	const query = `SELECT id, object_type, logical_key, report_job_id, template_id, template_version, sha256, size_bytes, created_at FROM storage_objects WHERE id = $1`
	object, err := scanObject(r.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, fmt.Errorf("get storage object: %w", err)
	}
	object.Placements, err = r.ListPlacements(ctx, id)
	return object, err
}

func (r *Repository) AddPlacement(ctx context.Context, placement Placement) (Placement, error) {
	if placement.ID == "" {
		placement.ID = ulid.Make().String()
	}
	const query = `
		INSERT INTO storage_object_placements (id, object_id, profile_id, storage_key, state, sha256, size_bytes, verified_at, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, object_id, profile_id, storage_key, state, sha256, size_bytes, created_at, verified_at, error`
	result, err := scanPlacement(r.pool.QueryRow(ctx, query, placement.ID, placement.ObjectID, placement.ProfileID, placement.StorageKey, placement.State, placement.SHA256, placement.SizeBytes, placement.VerifiedAt, placement.Error))
	if err != nil {
		return Placement{}, fmt.Errorf("add object placement: %w", err)
	}
	return result, nil
}

func (r *Repository) ListPlacements(ctx context.Context, objectID string) ([]Placement, error) {
	const query = `SELECT id, object_id, profile_id, storage_key, state, sha256, size_bytes, created_at, verified_at, error FROM storage_object_placements WHERE object_id = $1 ORDER BY created_at, id`
	rows, err := r.pool.Query(ctx, query, objectID)
	if err != nil {
		return nil, fmt.Errorf("list object placements: %w", err)
	}
	defer rows.Close()
	var placements []Placement
	for rows.Next() {
		placement, err := scanPlacement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan object placement: %w", err)
		}
		placements = append(placements, placement)
	}
	return placements, rows.Err()
}

func (r *Repository) ReportPlacements(ctx context.Context, jobID, preferredProfileID string) ([]Placement, error) {
	return r.availablePlacements(ctx, `o.report_job_id = $1`, preferredProfileID, jobID)
}

func (r *Repository) TemplatePlacements(ctx context.Context, templateID int64, version int, preferredProfileID string) ([]Placement, error) {
	return r.availablePlacements(ctx, `o.template_id = $1 AND o.template_version = $2`, preferredProfileID, templateID, version)
}

func (r *Repository) availablePlacements(ctx context.Context, condition, preferredProfileID string, arguments ...any) ([]Placement, error) {
	preferredIndex := len(arguments) + 1
	query := fmt.Sprintf(`
		SELECT p.id, p.object_id, p.profile_id, p.storage_key, p.state, p.sha256, p.size_bytes, p.created_at, p.verified_at, p.error
		FROM storage_objects o JOIN storage_object_placements p ON p.object_id = o.id
		WHERE %s AND p.state = 'available'
		ORDER BY CASE WHEN p.profile_id = $%d THEN 0 ELSE 1 END, p.verified_at DESC NULLS LAST, p.created_at`, condition, preferredIndex)
	arguments = append(arguments, preferredProfileID)
	rows, err := r.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list available object placements: %w", err)
	}
	defer rows.Close()
	var placements []Placement
	for rows.Next() {
		placement, err := scanPlacement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan available object placement: %w", err)
		}
		placements = append(placements, placement)
	}
	return placements, rows.Err()
}

func (r *Repository) RecordTemplateArtifact(ctx context.Context, templateID int64, version int, profileID string, result storage.Result, size int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin template artifact metadata: %w", err)
	}
	defer tx.Rollback(ctx)
	objectID := ulid.Make().String()
	if err := tx.QueryRow(ctx, `
		INSERT INTO storage_objects (id, object_type, logical_key, template_id, template_version, sha256, size_bytes)
		VALUES ($1, 'template', $2, $3, $4, $5, $6)
		ON CONFLICT (template_id, template_version) DO UPDATE
		SET logical_key = EXCLUDED.logical_key, sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes
		RETURNING id`, objectID, result.Key, templateID, version, result.SHA256, size).Scan(&objectID); err != nil {
		return fmt.Errorf("upsert template storage object: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO storage_object_placements (id, object_id, profile_id, storage_key, state, sha256, size_bytes, verified_at)
		VALUES ($1, $2, $3, $4, 'available', $5, $6, now())
		ON CONFLICT (object_id, profile_id) DO UPDATE
		SET storage_key = EXCLUDED.storage_key, state = 'available', sha256 = EXCLUDED.sha256,
		    size_bytes = EXCLUDED.size_bytes, verified_at = now(), error = NULL`,
		ulid.Make().String(), objectID, profileID, result.Key, result.SHA256, size)
	if err != nil {
		return fmt.Errorf("upsert template placement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit template artifact metadata: %w", err)
	}
	return nil
}

func (r *Repository) CreateMigration(ctx context.Context, sourceProfileID, destinationProfileID string, objectIDs []string) (Migration, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Migration{}, fmt.Errorf("begin storage migration creation: %w", err)
	}
	defer tx.Rollback(ctx)
	id := ulid.Make().String()
	const query = `INSERT INTO storage_migrations (id, source_profile_id, destination_profile_id, state) VALUES ($1, $2, $3, 'draft') RETURNING id, source_profile_id, destination_profile_id, state, created_at, started_at, completed_at, error`
	migration, err := scanMigration(tx.QueryRow(ctx, query, id, sourceProfileID, destinationProfileID))
	if err != nil {
		return Migration{}, fmt.Errorf("create storage migration: %w", err)
	}
	if len(objectIDs) == 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO storage_migration_items
				(migration_id, object_id, source_placement_id, storage_key, expected_sha256, expected_size_bytes)
			SELECT $1, p.object_id, p.id, p.storage_key, COALESCE(p.sha256, o.sha256), COALESCE(p.size_bytes, o.size_bytes)
			FROM storage_object_placements p JOIN storage_objects o ON o.id = p.object_id
			WHERE p.profile_id = $2 AND p.state = 'available'`, id, sourceProfileID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO storage_migration_items
				(migration_id, object_id, source_placement_id, storage_key, expected_sha256, expected_size_bytes)
			SELECT $1, p.object_id, p.id, p.storage_key, COALESCE(p.sha256, o.sha256), COALESCE(p.size_bytes, o.size_bytes)
			FROM storage_object_placements p JOIN storage_objects o ON o.id = p.object_id
			WHERE p.profile_id = $2 AND p.state = 'available' AND p.object_id = ANY($3)`, id, sourceProfileID, objectIDs)
	}
	if err != nil {
		return Migration{}, fmt.Errorf("snapshot storage migration items: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Migration{}, fmt.Errorf("commit storage migration creation: %w", err)
	}
	return migration, nil
}

func (r *Repository) GetMigration(ctx context.Context, id string) (Migration, []MigrationItem, error) {
	const query = `SELECT id, source_profile_id, destination_profile_id, state, created_at, started_at, completed_at, error FROM storage_migrations WHERE id = $1`
	migration, err := scanMigration(r.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, nil, ErrNotFound
	}
	if err != nil {
		return Migration{}, nil, fmt.Errorf("get storage migration: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT migration_id, object_id, state, attempts, started_at, completed_at, error, storage_key FROM storage_migration_items WHERE migration_id = $1 ORDER BY object_id`, id)
	if err != nil {
		return Migration{}, nil, fmt.Errorf("list storage migration items: %w", err)
	}
	defer rows.Close()
	var items []MigrationItem
	for rows.Next() {
		var item MigrationItem
		if err := rows.Scan(&item.MigrationID, &item.ObjectID, &item.State, &item.Attempts, &item.StartedAt, &item.CompletedAt, &item.Error, &item.StorageKey); err != nil {
			return Migration{}, nil, fmt.Errorf("scan storage migration item: %w", err)
		}
		items = append(items, item)
	}
	migration.TotalItems = len(items)
	for _, item := range items {
		if item.State == "completed" || item.State == "skipped" {
			migration.CompletedItems++
		}
		if item.State == "failed" {
			migration.FailedItems++
		}
	}
	return migration, items, rows.Err()
}

func (r *Repository) ListMigrations(ctx context.Context) ([]Migration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.id, m.source_profile_id, m.destination_profile_id, m.state, m.created_at, m.started_at, m.completed_at, m.error,
		       count(i.object_id), count(i.object_id) FILTER (WHERE i.state IN ('completed','skipped')),
		       count(i.object_id) FILTER (WHERE i.state = 'failed')
		FROM storage_migrations m LEFT JOIN storage_migration_items i ON i.migration_id = m.id
		GROUP BY m.id ORDER BY m.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list storage migrations: %w", err)
	}
	defer rows.Close()
	result := make([]Migration, 0)
	for rows.Next() {
		var item Migration
		if err := rows.Scan(&item.ID, &item.SourceProfileID, &item.DestinationProfileID, &item.State, &item.CreatedAt, &item.StartedAt, &item.CompletedAt, &item.Error, &item.TotalItems, &item.CompletedItems, &item.FailedItems); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type MigrationWork struct {
	MigrationID, ObjectID, SourceProfileID, DestinationProfileID, StorageKey string
	ExpectedSHA256                                                           *string
	ExpectedSizeBytes                                                        *int64
	Attempts                                                                 int
}

// ClaimMigrationWork enforces per-profile limits under a transaction advisory lock.
func (r *Repository) ClaimMigrationWork(ctx context.Context, lease time.Duration) (*MigrationWork, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE storage_migration_items i SET state='pending',lease_until=NULL,available_at=now() FROM storage_migrations m WHERE m.id=i.migration_id AND m.state='running' AND i.state='running' AND i.lease_until < now()`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT m.destination_profile_id FROM storage_migrations m JOIN storage_migration_items i ON i.migration_id=m.id WHERE m.state='running' AND i.state='pending' AND i.available_at <= now() ORDER BY m.destination_profile_id`)
	if err != nil {
		return nil, err
	}
	var profiles []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		profiles = append(profiles, id)
	}
	rows.Close()
	for _, profileID := range profiles {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`, profileID).Scan(&locked); err != nil || !locked {
			continue
		}
		var concurrency int
		var rate *int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(transfer_concurrency,1), request_rate_limit FROM storage_profiles WHERE id=$1`, profileID).Scan(&concurrency, &rate); err != nil {
			return nil, err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM storage_migration_items i JOIN storage_migrations m ON m.id=i.migration_id WHERE m.destination_profile_id=$1 AND i.state='running' AND i.lease_until > now()`, profileID).Scan(&active); err != nil {
			return nil, err
		}
		if active >= concurrency {
			continue
		}
		var work MigrationWork
		err := tx.QueryRow(ctx, `
			SELECT i.migration_id, i.object_id, m.source_profile_id, m.destination_profile_id, i.storage_key, i.expected_sha256, i.expected_size_bytes, i.attempts + 1
			FROM storage_migration_items i JOIN storage_migrations m ON m.id=i.migration_id
			LEFT JOIN storage_profile_dispatch d ON d.profile_id=m.destination_profile_id
			WHERE m.destination_profile_id=$1 AND m.state='running' AND i.state='pending' AND i.available_at <= now()
			  AND COALESCE(d.next_available_at, now()) <= now()
			ORDER BY i.available_at, i.object_id LIMIT 1 FOR UPDATE OF i SKIP LOCKED`, profileID).Scan(&work.MigrationID, &work.ObjectID, &work.SourceProfileID, &work.DestinationProfileID, &work.StorageKey, &work.ExpectedSHA256, &work.ExpectedSizeBytes, &work.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE storage_migration_items SET state='running', attempts=attempts+1, started_at=COALESCE(started_at,now()), completed_at=NULL, error=NULL, lease_until=now()+make_interval(secs=>$3) WHERE migration_id=$1 AND object_id=$2`, work.MigrationID, work.ObjectID, lease.Seconds())
		if err != nil {
			return nil, err
		}
		if rate != nil {
			_, err = tx.Exec(ctx, `INSERT INTO storage_profile_dispatch(profile_id,next_available_at) VALUES($1,now()+make_interval(secs=>$2)) ON CONFLICT(profile_id) DO UPDATE SET next_available_at=EXCLUDED.next_available_at`, profileID, 1.0/float64(*rate))
			if err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &work, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *Repository) GetMigrationWork(ctx context.Context, migrationID, objectID string) (MigrationWork, bool, error) {
	var work MigrationWork
	var migrationState, itemState string
	err := r.pool.QueryRow(ctx, `SELECT i.migration_id,i.object_id,m.source_profile_id,m.destination_profile_id,i.storage_key,i.expected_sha256,i.expected_size_bytes,i.attempts,m.state,i.state FROM storage_migration_items i JOIN storage_migrations m ON m.id=i.migration_id WHERE i.migration_id=$1 AND i.object_id=$2`, migrationID, objectID).Scan(&work.MigrationID, &work.ObjectID, &work.SourceProfileID, &work.DestinationProfileID, &work.StorageKey, &work.ExpectedSHA256, &work.ExpectedSizeBytes, &work.Attempts, &migrationState, &itemState)
	if errors.Is(err, pgx.ErrNoRows) {
		return MigrationWork{}, false, ErrNotFound
	}
	if err != nil {
		return MigrationWork{}, false, err
	}
	return work, migrationState == "running" && itemState == "running", nil
}

func (r *Repository) ReleaseMigrationWork(ctx context.Context, migrationID, objectID, message string, retry bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state := "failed"
	if retry {
		state = "pending"
	}
	_, err = tx.Exec(ctx, `UPDATE storage_migration_items SET state=$3, error=$4, lease_until=NULL, available_at=now()+make_interval(secs=>CASE WHEN $5 THEN LEAST(power(2,attempts-1),60) ELSE 0 END) WHERE migration_id=$1 AND object_id=$2`, migrationID, objectID, state, message, retry)
	if err != nil {
		return err
	}
	if !retry {
		if _, err = tx.Exec(ctx, `UPDATE storage_migrations SET state='failed',completed_at=now(),error=$2 WHERE id=$1 AND state='running'`, migrationID, message); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) CompleteMigrationWork(ctx context.Context, work MigrationWork, digest string, size int64, skipped bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !skipped {
		_, err = tx.Exec(ctx, `INSERT INTO storage_object_placements(id,object_id,profile_id,storage_key,state,sha256,size_bytes,verified_at) VALUES($1,$2,$3,$4,'available',$5,$6,now()) ON CONFLICT(object_id,profile_id) DO UPDATE SET storage_key=EXCLUDED.storage_key,state='available',sha256=EXCLUDED.sha256,size_bytes=EXCLUDED.size_bytes,verified_at=now(),error=NULL`, ulid.Make().String(), work.ObjectID, work.DestinationProfileID, work.StorageKey, digest, size)
		if err != nil {
			return err
		}
	}
	state := "completed"
	if skipped {
		state = "skipped"
	}
	if _, err = tx.Exec(ctx, `UPDATE storage_migration_items SET state=$3,completed_at=now(),error=NULL,lease_until=NULL WHERE migration_id=$1 AND object_id=$2`, work.MigrationID, work.ObjectID, state); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE storage_migrations m SET state='completed',completed_at=now(),error=NULL WHERE id=$1 AND state='running' AND NOT EXISTS(SELECT 1 FROM storage_migration_items WHERE migration_id=m.id AND state NOT IN ('completed','skipped'))`, work.MigrationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) DestinationAvailable(ctx context.Context, objectID, profileID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_object_placements WHERE object_id=$1 AND profile_id=$2 AND state='available' AND verified_at IS NOT NULL)`, objectID, profileID).Scan(&exists)
	return exists, err
}

func (r *Repository) ResumeMigration(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE storage_migrations SET state='running',started_at=COALESCE(started_at,now()),completed_at=NULL,error=NULL WHERE id=$1 AND state IN ('draft','paused','failed')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = r.pool.Exec(ctx, `UPDATE storage_migration_items SET state='pending',available_at=now(),error=NULL,lease_until=NULL WHERE migration_id=$1 AND state='failed'`, id)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `UPDATE storage_migrations m SET state='completed',completed_at=now() WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM storage_migration_items WHERE migration_id=m.id AND state NOT IN ('completed','skipped'))`, id)
	return err
}

func (r *Repository) PauseMigration(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE storage_migrations SET state='paused' WHERE id=$1 AND state='running'`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) CancelMigration(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE storage_migrations SET state='canceled',completed_at=now() WHERE id=$1 AND state IN ('draft','running','paused','failed')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) SetMigrationState(ctx context.Context, id, state string, message *string) error {
	const command = `
		UPDATE storage_migrations
		SET state = $2,
		    started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE WHEN $2 IN ('completed', 'canceled', 'failed') THEN now() ELSE NULL END,
		    error = $3
		WHERE id = $1`
	tag, err := r.pool.Exec(ctx, command, id, state, message)
	if err != nil {
		return fmt.Errorf("set storage migration state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) SetMigrationItemState(ctx context.Context, migrationID, objectID, state string, message *string) error {
	const command = `
		UPDATE storage_migration_items
		SET state = $3,
		    attempts = attempts + CASE WHEN $3 = 'running' THEN 1 ELSE 0 END,
		    started_at = CASE WHEN $3 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE WHEN $3 IN ('completed', 'skipped', 'failed') THEN now() ELSE NULL END,
		    error = $4
		WHERE migration_id = $1 AND object_id = $2`
	tag, err := r.pool.Exec(ctx, command, migrationID, objectID, state, message)
	if err != nil {
		return fmt.Errorf("set storage migration item state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// BootstrapLegacy records artifacts written by the pre-control-plane runtime.
// It is idempotent and does not modify report_jobs or template_versions.
func (r *Repository) BootstrapLegacy(ctx context.Context, input ProfileInput) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin legacy storage bootstrap: %w", err)
	}
	defer tx.Rollback(ctx)
	encrypted, err := r.encryptCredentials(LegacyProfileID, input.Credentials)
	if err != nil {
		return err
	}
	config := input.PublicConfig
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if input.Name == "" {
		input.Name = "legacy-runtime-storage"
	}
	if input.State == "" {
		input.State = "active"
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO storage_profiles (id, name, backend_type, state, public_config, encrypted_credentials, request_rate_limit, transfer_concurrency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		LegacyProfileID, input.Name, input.BackendType, input.State, config, encrypted, input.RequestRateLimit, input.TransferConcurrency)
	if err != nil {
		return fmt.Errorf("create legacy storage profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var backend string
		var existingConfig json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT backend_type, public_config FROM storage_profiles WHERE id = $1`, LegacyProfileID).Scan(&backend, &existingConfig); err != nil {
			return fmt.Errorf("read legacy storage profile: %w", err)
		}
		var sameConfig bool
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, existingConfig, config).Scan(&sameConfig); err != nil {
			return fmt.Errorf("compare legacy storage profile: %w", err)
		}
		if backend != input.BackendType || !sameConfig {
			return fmt.Errorf("legacy storage profile already exists with different runtime configuration")
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO storage_defaults (singleton, profile_id) VALUES (true, $1) ON CONFLICT (singleton) DO NOTHING`, LegacyProfileID); err != nil {
		return fmt.Errorf("initialize default storage profile: %w", err)
	}
	if err := r.bootstrapTemplates(ctx, tx); err != nil {
		return err
	}
	if err := r.bootstrapReports(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit legacy storage bootstrap: %w", err)
	}
	return nil
}

func (r *Repository) bootstrapTemplates(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		SELECT tv.template_id, tv.version, tv.storage_key
		FROM template_versions tv
		WHERE tv.status = 'published'
		  AND NOT EXISTS (
		      SELECT 1 FROM storage_objects o
		      WHERE o.template_id = tv.template_id AND o.template_version = tv.version
		  )
		ORDER BY tv.template_id, tv.version`)
	if err != nil {
		return fmt.Errorf("list legacy template artifacts: %w", err)
	}
	type artifact struct {
		templateID int64
		version    int
		key        string
	}
	var artifacts []artifact
	for rows.Next() {
		var item artifact
		if err := rows.Scan(&item.templateID, &item.version, &item.key); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy template artifact: %w", err)
		}
		artifacts = append(artifacts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range artifacts {
		objectID := ulid.Make().String()
		if err := tx.QueryRow(ctx, `
			INSERT INTO storage_objects (id, object_type, logical_key, template_id, template_version)
			VALUES ($1, 'template', $2, $3, $4)
			ON CONFLICT (template_id, template_version) DO UPDATE SET logical_key = storage_objects.logical_key
			RETURNING id`, objectID, item.key, item.templateID, item.version).Scan(&objectID); err != nil {
			return fmt.Errorf("bootstrap template object: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO storage_object_placements (id, object_id, profile_id, storage_key, state)
			VALUES ($1, $2, $3, $4, 'available') ON CONFLICT (object_id, profile_id) DO NOTHING`,
			ulid.Make().String(), objectID, LegacyProfileID, item.key); err != nil {
			return fmt.Errorf("bootstrap template placement: %w", err)
		}
	}
	return nil
}

func (r *Repository) bootstrapReports(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		SELECT j.id, j.storage_key, j.sha256
		FROM report_jobs j
		WHERE j.status = 'completed' AND j.storage_key IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM storage_objects o WHERE o.report_job_id = j.id)
		ORDER BY j.id`)
	if err != nil {
		return fmt.Errorf("list legacy report artifacts: %w", err)
	}
	type artifact struct {
		jobID  string
		key    string
		sha256 *string
	}
	var artifacts []artifact
	for rows.Next() {
		var item artifact
		if err := rows.Scan(&item.jobID, &item.key, &item.sha256); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy report artifact: %w", err)
		}
		artifacts = append(artifacts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range artifacts {
		objectID := ulid.Make().String()
		if err := tx.QueryRow(ctx, `
			INSERT INTO storage_objects (id, object_type, logical_key, report_job_id, sha256)
			VALUES ($1, 'report', $2, $3, $4)
			ON CONFLICT (report_job_id) DO UPDATE SET logical_key = storage_objects.logical_key
			RETURNING id`, objectID, item.key, item.jobID, item.sha256).Scan(&objectID); err != nil {
			return fmt.Errorf("bootstrap report object: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO storage_object_placements (id, object_id, profile_id, storage_key, state, sha256)
			VALUES ($1, $2, $3, $4, 'available', $5) ON CONFLICT (object_id, profile_id) DO NOTHING`,
			ulid.Make().String(), objectID, LegacyProfileID, item.key, item.sha256); err != nil {
			return fmt.Errorf("bootstrap report placement: %w", err)
		}
	}
	return nil
}

func (r *Repository) encryptCredentials(id string, credentials json.RawMessage) ([]byte, error) {
	if len(credentials) == 0 {
		return nil, nil
	}
	if r.cipher == nil {
		return nil, fmt.Errorf("credential cipher is required when credentials are configured")
	}
	encrypted, err := r.cipher.Encrypt(credentials, []byte(id))
	if err != nil {
		return nil, fmt.Errorf("encrypt storage credentials: %w", err)
	}
	return encrypted, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(row scanner) (Profile, error) {
	var profile Profile
	err := row.Scan(&profile.ID, &profile.Name, &profile.BackendType, &profile.State, &profile.PublicConfig,
		&profile.CredentialConfigured, &profile.RequestRateLimit, &profile.TransferConcurrency, &profile.CreatedAt)
	return profile, err
}

func scanObject(row scanner) (Object, error) {
	var object Object
	err := row.Scan(&object.ID, &object.ObjectType, &object.LogicalKey, &object.ReportJobID, &object.TemplateID,
		&object.TemplateVersion, &object.SHA256, &object.SizeBytes, &object.CreatedAt)
	return object, err
}

func scanPlacement(row scanner) (Placement, error) {
	var placement Placement
	err := row.Scan(&placement.ID, &placement.ObjectID, &placement.ProfileID, &placement.StorageKey, &placement.State,
		&placement.SHA256, &placement.SizeBytes, &placement.CreatedAt, &placement.VerifiedAt, &placement.Error)
	return placement, err
}

func scanMigration(row scanner) (Migration, error) {
	var migration Migration
	err := row.Scan(&migration.ID, &migration.SourceProfileID, &migration.DestinationProfileID, &migration.State,
		&migration.CreatedAt, &migration.StartedAt, &migration.CompletedAt, &migration.Error)
	return migration, err
}
