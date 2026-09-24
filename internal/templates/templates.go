package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("template not found")
	ErrConflict = errors.New("template conflict")
)

type Template struct {
	Slug       string           `json:"slug"`
	Name       string           `json:"name"`
	CreatedAt  time.Time        `json:"createdAt"`
	ArchivedAt *time.Time       `json:"archivedAt,omitempty"`
	ArchivedBy *string          `json:"archivedBy,omitempty"`
	Versions   []VersionSummary `json:"versions"`
}

type VersionSummary struct {
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	ApprovedAt  *time.Time `json:"approvedAt,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

type Version struct {
	VersionSummary
	TemplateID  int64           `json:"-"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Source      string          `json:"source"`
	SampleData  json.RawMessage `json:"sampleData"`
	DataSchema  json.RawMessage `json:"dataSchema"`
	SchemaHash  string          `json:"schemaHash"`
	StorageKey  string          `json:"storageKey"`
	CreatedBy   *string         `json:"createdBy,omitempty"`
	ApprovedBy  *string         `json:"approvedBy,omitempty"`
	PublishedBy *string         `json:"publishedBy,omitempty"`
}

type PublishedArtifact struct {
	StorageKey string
	Source     string
}

type PublishedCatalogTemplate struct {
	Slug          string                    `json:"slug"`
	Name          string                    `json:"name"`
	LatestVersion int                       `json:"latestVersion"`
	Versions      []PublishedCatalogVersion `json:"versions"`
}

type PublishedCatalogVersion struct {
	Version     int             `json:"version"`
	PublishedAt time.Time       `json:"publishedAt"`
	DataSchema  json.RawMessage `json:"dataSchema"`
	SchemaHash  string          `json:"schemaHash"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) List(ctx context.Context) ([]Template, error) {
	const query = `
		SELECT t.slug, t.name, t.created_at, t.archived_at, t.archived_by,
		       tv.version, tv.status, tv.created_at, tv.approved_at, tv.published_at
		FROM templates t
		LEFT JOIN template_versions tv ON tv.template_id = t.id
		ORDER BY t.name, t.slug, tv.version DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()

	var result []Template
	var current *Template
	for rows.Next() {
		var slug, name string
		var templateCreated time.Time
		var archivedAt *time.Time
		var archivedBy *string
		var version *int
		var status *string
		var versionCreated, approvedAt, publishedAt *time.Time
		if err := rows.Scan(&slug, &name, &templateCreated, &archivedAt, &archivedBy, &version, &status, &versionCreated, &approvedAt, &publishedAt); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		if current == nil || current.Slug != slug {
			result = append(result, Template{Slug: slug, Name: name, CreatedAt: templateCreated, ArchivedAt: archivedAt, ArchivedBy: archivedBy, Versions: []VersionSummary{}})
			current = &result[len(result)-1]
		}
		if version != nil {
			current.Versions = append(current.Versions, VersionSummary{
				Version: *version, Status: *status, CreatedAt: *versionCreated,
				ApprovedAt: approvedAt, PublishedAt: publishedAt,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	return result, nil
}

func (r *Repository) SetArchived(ctx context.Context, slug, actor string, archived bool) error {
	var command string
	var args []any
	if archived {
		command = `UPDATE templates SET archived_at = COALESCE(archived_at, now()), archived_by = CASE WHEN archived_at IS NULL THEN NULLIF($2, '') ELSE archived_by END WHERE slug = $1`
		args = []any{slug, actor}
	} else {
		command = `UPDATE templates SET archived_at = NULL, archived_by = NULL WHERE slug = $1`
		args = []any{slug}
	}
	tag, err := r.pool.Exec(ctx, command, args...)
	if err != nil {
		return fmt.Errorf("set template archive state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) GetVersion(ctx context.Context, slug string, version int) (Version, error) {
	const query = `
		SELECT t.id, t.slug, t.name, tv.version, tv.status, tv.source, tv.sample_data, tv.data_schema, tv.schema_sha256, tv.storage_key,
		       tv.created_at, tv.approved_at, tv.published_at, tv.created_by, tv.approved_by, tv.published_by
		FROM templates t JOIN template_versions tv ON tv.template_id = t.id
		WHERE t.slug = $1 AND tv.version = $2`
	var result Version
	err := r.pool.QueryRow(ctx, query, slug, version).Scan(
		&result.TemplateID, &result.Slug, &result.Name, &result.Version, &result.Status, &result.Source,
		&result.SampleData, &result.DataSchema, &result.SchemaHash, &result.StorageKey, &result.CreatedAt, &result.ApprovedAt,
		&result.PublishedAt, &result.CreatedBy, &result.ApprovedBy, &result.PublishedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("get template version: %w", err)
	}
	return result, nil
}

func (r *Repository) Create(ctx context.Context, slug, name, source string, sampleData, dataSchema json.RawMessage, schemaHash, actor string) (Version, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("begin template creation: %w", err)
	}
	defer tx.Rollback(ctx)
	var templateID int64
	if err := tx.QueryRow(ctx, `INSERT INTO templates (slug, name) VALUES ($1, $2) RETURNING id`, slug, name).Scan(&templateID); err != nil {
		return Version{}, mapWriteError("create template", err)
	}
	key := fmt.Sprintf("templates/%s/v1/main.typ", slug)
	_, err = tx.Exec(ctx, `INSERT INTO template_versions (template_id, version, status, source, sample_data, data_schema, schema_sha256, storage_key, created_by) VALUES ($1, 1, 'draft', $2, $3, $4, $5, $6, NULLIF($7, ''))`, templateID, source, sampleData, dataSchema, schemaHash, key, actor)
	if err != nil {
		return Version{}, mapWriteError("create template version", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, fmt.Errorf("commit template creation: %w", err)
	}
	return r.GetVersion(ctx, slug, 1)
}

func (r *Repository) CreateVersion(ctx context.Context, slug, source string, sampleData, dataSchema json.RawMessage, schemaHash, actor string) (Version, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Version{}, fmt.Errorf("begin version creation: %w", err)
	}
	defer tx.Rollback(ctx)
	var templateID int64
	var version int
	err = tx.QueryRow(ctx, `SELECT id, COALESCE((SELECT max(version) FROM template_versions WHERE template_id = templates.id), 0) + 1 FROM templates WHERE slug = $1 FOR UPDATE`, slug).Scan(&templateID, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("lock template: %w", err)
	}
	key := fmt.Sprintf("templates/%s/v%d/main.typ", slug, version)
	_, err = tx.Exec(ctx, `INSERT INTO template_versions (template_id, version, status, source, sample_data, data_schema, schema_sha256, storage_key, created_by) VALUES ($1, $2, 'draft', $3, $4, $5, $6, $7, NULLIF($8, ''))`, templateID, version, source, sampleData, dataSchema, schemaHash, key, actor)
	if err != nil {
		return Version{}, mapWriteError("create template version", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Version{}, fmt.Errorf("commit version creation: %w", err)
	}
	return r.GetVersion(ctx, slug, version)
}

func (r *Repository) UpdateDraft(ctx context.Context, slug string, version int, source string, sampleData, dataSchema json.RawMessage, schemaHash string) (Version, error) {
	const command = `UPDATE template_versions tv SET source = $3, sample_data = $4, data_schema = $5, schema_sha256 = $6 FROM templates t WHERE tv.template_id = t.id AND t.slug = $1 AND tv.version = $2 AND tv.status = 'draft'`
	tag, err := r.pool.Exec(ctx, command, slug, version, source, sampleData, dataSchema, schemaHash)
	if err != nil {
		return Version{}, mapWriteError("update draft", err)
	}
	if tag.RowsAffected() == 0 {
		return Version{}, ErrConflict
	}
	return r.GetVersion(ctx, slug, version)
}

func (r *Repository) DeleteDraft(ctx context.Context, slug string, version int) error {
	const command = `DELETE FROM template_versions tv USING templates t WHERE tv.template_id = t.id AND t.slug = $1 AND tv.version = $2 AND tv.status = 'draft'`
	tag, err := r.pool.Exec(ctx, command, slug, version)
	if err != nil {
		return mapWriteError("delete draft", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) Approve(ctx context.Context, slug string, version int, source string, sampleData, dataSchema json.RawMessage, schemaHash, actor string) (Version, error) {
	const command = `UPDATE template_versions tv SET status = 'approved', sample_data = $5, data_schema = $6, schema_sha256 = $7, approved_at = now(), approved_by = NULLIF($8, '') FROM templates t WHERE tv.template_id = t.id AND t.slug = $1 AND tv.version = $2 AND tv.status = 'draft' AND tv.source = $3 AND tv.sample_data = $4::jsonb`
	tag, err := r.pool.Exec(ctx, command, slug, version, source, sampleData, sampleData, dataSchema, schemaHash, actor)
	if err != nil {
		return Version{}, mapWriteError("approve template", err)
	}
	if tag.RowsAffected() == 0 {
		return Version{}, ErrConflict
	}
	return r.GetVersion(ctx, slug, version)
}

func (r *Repository) Publish(ctx context.Context, slug string, version int, actor string) (Version, error) {
	const command = `UPDATE template_versions tv SET status = 'published', published_at = now(), published_by = NULLIF($3, '') FROM templates t WHERE tv.template_id = t.id AND t.slug = $1 AND tv.version = $2 AND tv.status = 'approved'`
	tag, err := r.pool.Exec(ctx, command, slug, version, actor)
	if err != nil {
		return Version{}, mapWriteError("publish template", err)
	}
	if tag.RowsAffected() == 0 {
		return Version{}, ErrConflict
	}
	return r.GetVersion(ctx, slug, version)
}

func (r *Repository) PublishedArtifacts(ctx context.Context, profileID string) ([]PublishedArtifact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.storage_key, tv.source
		FROM template_versions tv
		JOIN storage_objects o ON o.template_id = tv.template_id AND o.template_version = tv.version
		JOIN storage_object_placements p ON p.object_id = o.id
		WHERE tv.status = 'published' AND p.profile_id = $1 AND p.state = 'available'
		ORDER BY tv.template_id, tv.version`, profileID)
	if err != nil {
		return nil, fmt.Errorf("list published templates: %w", err)
	}
	defer rows.Close()
	var artifacts []PublishedArtifact
	for rows.Next() {
		var artifact PublishedArtifact
		if err := rows.Scan(&artifact.StorageKey, &artifact.Source); err != nil {
			return nil, fmt.Errorf("scan published template: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (r *Repository) PublishedCatalog(ctx context.Context) ([]PublishedCatalogTemplate, error) {
	const query = `
		SELECT t.slug, t.name, tv.version, tv.published_at, tv.data_schema, tv.schema_sha256
		FROM templates t JOIN template_versions tv ON tv.template_id = t.id
		WHERE tv.status = 'published' AND t.archived_at IS NULL
		ORDER BY t.name, t.slug, tv.version DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list published template catalog: %w", err)
	}
	defer rows.Close()

	catalog := make([]PublishedCatalogTemplate, 0)
	var current *PublishedCatalogTemplate
	for rows.Next() {
		var slug, name string
		var version int
		var publishedAt time.Time
		var dataSchema json.RawMessage
		var schemaHash string
		if err := rows.Scan(&slug, &name, &version, &publishedAt, &dataSchema, &schemaHash); err != nil {
			return nil, fmt.Errorf("scan published template catalog: %w", err)
		}
		if current == nil || current.Slug != slug {
			catalog = append(catalog, PublishedCatalogTemplate{
				Slug: slug, Name: name, LatestVersion: version, Versions: []PublishedCatalogVersion{},
			})
			current = &catalog[len(catalog)-1]
		}
		current.Versions = append(current.Versions, PublishedCatalogVersion{Version: version, PublishedAt: publishedAt, DataSchema: dataSchema, SchemaHash: schemaHash})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list published template catalog: %w", err)
	}
	return catalog, nil
}

func mapWriteError(action string, err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && (pgError.Code == "23505" || pgError.Code == "23514") {
		return fmt.Errorf("%s: %w", action, ErrConflict)
	}
	return fmt.Errorf("%s: %w", action, err)
}
