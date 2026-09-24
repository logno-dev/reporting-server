package apiclients

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

const (
	ScopeTemplatesRead   = "templates:read"
	ScopeReportsSubmit   = "reports:submit"
	ScopeReportsRead     = "reports:read"
	ScopeReportsDownload = "reports:download"
	keyPrefix            = "rpt_live_"
)

var (
	ErrNotFound     = errors.New("API client or key not found")
	ErrConflict     = errors.New("API client conflict")
	ErrInvalidKey   = errors.New("invalid API key")
	SupportedScopes = []string{ScopeTemplatesRead, ScopeReportsSubmit, ScopeReportsRead, ScopeReportsDownload}
)

type Client struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy"`
	DisabledAt  *time.Time `json:"disabledAt,omitempty"`
	DisabledBy  *string    `json:"disabledBy,omitempty"`
}

type Key struct {
	ID           string     `json:"id"`
	ClientID     string     `json:"clientId"`
	Label        string     `json:"label"`
	Prefix       string     `json:"prefix"`
	Scopes       []string   `json:"scopes"`
	IssuedAt     time.Time  `json:"issuedAt"`
	IssuedBy     string     `json:"issuedBy"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	RevokedBy    *string    `json:"revokedBy,omitempty"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
	LastUsedIP   *string    `json:"lastUsedIp,omitempty"`
	ReplacesID   *string    `json:"replacesId,omitempty"`
	ReplacedByID *string    `json:"replacedById,omitempty"`
	Status       string     `json:"status"`
}

type IssuedKey struct {
	Key
	Secret string `json:"secret"`
}

type Identity struct {
	ClientID   string
	ClientName string
	KeyID      string
	Scopes     []string
}

type Service struct {
	pool          *pgxpool.Pool
	now           func() time.Time
	usageInterval time.Duration
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now, usageInterval: 5 * time.Minute}
}

func ValidateScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, errors.New("at least one scope is required")
	}
	clean := make([]string, 0, len(scopes))
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !slices.Contains(SupportedScopes, scope) || seen[scope] {
			return nil, fmt.Errorf("unsupported or duplicate scope %q", scope)
		}
		seen[scope] = true
		clean = append(clean, scope)
	}
	slices.Sort(clean)
	return clean, nil
}

func ParseKey(value string) (string, []byte, error) {
	if !strings.HasPrefix(value, keyPrefix) {
		return "", nil, ErrInvalidKey
	}
	parts := strings.SplitN(value, "_", 4)
	if len(parts) != 4 || parts[0] != "rpt" || parts[1] != "live" {
		return "", nil, ErrInvalidKey
	}
	if _, err := ulid.ParseStrict(parts[2]); err != nil {
		return "", nil, ErrInvalidKey
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != parts[3] {
		return "", nil, ErrInvalidKey
	}
	return parts[2], secret, nil
}

func (s *Service) CreateClient(ctx context.Context, name, description string, scopes []string, actor string) (Client, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	validated, err := ValidateScopes(scopes)
	if err != nil || name == "" || len(name) > 120 || len(description) > 1000 {
		return Client{}, errors.New("invalid API client")
	}
	client := Client{ID: ulid.Make().String(), Name: name, Description: description, Enabled: true, Scopes: validated, CreatedBy: actor}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Client{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO api_clients (id,name,description,scopes,created_by) VALUES ($1,$2,$3,$4,$5) RETURNING created_at`, client.ID, client.Name, client.Description, client.Scopes, actor).Scan(&client.CreatedAt)
	if err != nil {
		return Client{}, err
	}
	if err = audit(ctx, tx, client.ID, nil, "client.created", actor, `{}`); err != nil {
		return Client{}, err
	}
	return client, tx.Commit(ctx)
}

func (s *Service) ListClients(ctx context.Context) ([]Client, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,description,enabled,scopes,created_at,created_by,disabled_at,disabled_by FROM api_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Client, 0)
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Enabled, &c.Scopes, &c.CreatedAt, &c.CreatedBy, &c.DisabledAt, &c.DisabledBy); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Service) GetClient(ctx context.Context, id string) (Client, error) {
	var c Client
	err := s.pool.QueryRow(ctx, `SELECT id,name,description,enabled,scopes,created_at,created_by,disabled_at,disabled_by FROM api_clients WHERE id=$1`, id).Scan(&c.ID, &c.Name, &c.Description, &c.Enabled, &c.Scopes, &c.CreatedAt, &c.CreatedBy, &c.DisabledAt, &c.DisabledBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

func (s *Service) DisableClient(ctx context.Context, id, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE api_clients SET enabled=false,disabled_at=now(),disabled_by=$2 WHERE id=$1 AND enabled`, id, actor)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit(ctx, tx, id, nil, "client.disabled", actor, `{}`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) IssueKey(ctx context.Context, clientID, label string, expiresAt *time.Time, actor string) (IssuedKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IssuedKey{}, err
	}
	defer tx.Rollback(ctx)
	issued, err := s.issue(ctx, tx, clientID, label, expiresAt, nil, actor)
	if err != nil {
		return IssuedKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IssuedKey{}, err
	}
	return issued, nil
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, clientID, label string, expiresAt *time.Time, replaces *string, actor string) (IssuedKey, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 120 || (expiresAt != nil && !expiresAt.After(s.now())) {
		return IssuedKey{}, errors.New("invalid API key")
	}
	var scopes []string
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT scopes,enabled FROM api_clients WHERE id=$1 FOR UPDATE`, clientID).Scan(&scopes, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return IssuedKey{}, ErrNotFound
	}
	if err != nil {
		return IssuedKey{}, err
	}
	if !enabled {
		return IssuedKey{}, ErrConflict
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return IssuedKey{}, err
	}
	id := ulid.Make().String()
	plaintext := keyPrefix + id + "_" + base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256(secret)
	key := Key{ID: id, ClientID: clientID, Label: label, Prefix: keyPrefix + id + "_", Scopes: scopes, IssuedBy: actor, ExpiresAt: expiresAt, ReplacesID: replaces, Status: "active"}
	err = tx.QueryRow(ctx, `INSERT INTO api_keys(id,client_id,label,prefix,secret_hash,scopes,issued_by,expires_at,replaces_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING issued_at`, id, clientID, label, key.Prefix, digest[:], scopes, actor, expiresAt, replaces).Scan(&key.IssuedAt)
	if err != nil {
		return IssuedKey{}, err
	}
	if err := audit(ctx, tx, clientID, &id, "key.issued", actor, `{}`); err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{Key: key, Secret: plaintext}, nil
}

func (s *Service) RotateKey(ctx context.Context, clientID, keyID, label string, grace time.Duration, actor string) (IssuedKey, error) {
	if grace < 0 || grace > 30*24*time.Hour {
		return IssuedKey{}, errors.New("invalid rotation grace period")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IssuedKey{}, err
	}
	defer tx.Rollback(ctx)
	var oldClient string
	var revoked *time.Time
	var expires *time.Time
	var replacedBy *string
	if err := tx.QueryRow(ctx, `SELECT client_id,revoked_at,expires_at,replaced_by_id FROM api_keys WHERE id=$1 FOR UPDATE`, keyID).Scan(&oldClient, &revoked, &expires, &replacedBy); errors.Is(err, pgx.ErrNoRows) {
		return IssuedKey{}, ErrNotFound
	} else if err != nil {
		return IssuedKey{}, err
	}
	if oldClient != clientID || revoked != nil || replacedBy != nil || (expires != nil && !expires.After(s.now())) {
		return IssuedKey{}, ErrConflict
	}
	issued, err := s.issue(ctx, tx, clientID, label, nil, &keyID, actor)
	if err != nil {
		return IssuedKey{}, err
	}
	deadline := s.now().Add(grace)
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET replaced_by_id=$2,expires_at=CASE WHEN expires_at IS NULL OR expires_at>$3 THEN $3 ELSE expires_at END WHERE id=$1`, keyID, issued.ID, deadline); err != nil {
		return IssuedKey{}, err
	}
	if err := audit(ctx, tx, clientID, &keyID, "key.rotated", actor, fmt.Sprintf(`{"replacementKeyId":%q,"graceSeconds":%d}`, issued.ID, int64(grace.Seconds()))); err != nil {
		return IssuedKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IssuedKey{}, err
	}
	return issued, nil
}

func (s *Service) RevokeKey(ctx context.Context, clientID, keyID, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at=now(),revoked_by=$3 WHERE id=$1 AND client_id=$2 AND revoked_at IS NULL`, keyID, clientID, actor)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = audit(ctx, tx, clientID, &keyID, "key.revoked", actor, `{}`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ListKeys(ctx context.Context, clientID string) ([]Key, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_clients WHERE id=$1)`, clientID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT id,client_id,label,prefix,scopes,issued_at,issued_by,expires_at,revoked_at,revoked_by,last_used_at,last_used_ip::text,replaces_id,replaced_by_id FROM api_keys WHERE client_id=$1 ORDER BY issued_at DESC`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Key, 0)
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.ID, &k.ClientID, &k.Label, &k.Prefix, &k.Scopes, &k.IssuedAt, &k.IssuedBy, &k.ExpiresAt, &k.RevokedAt, &k.RevokedBy, &k.LastUsedAt, &k.LastUsedIP, &k.ReplacesID, &k.ReplacedByID); err != nil {
			return nil, err
		}
		k.Status = "active"
		if k.RevokedAt != nil {
			k.Status = "revoked"
		} else if k.ExpiresAt != nil && !k.ExpiresAt.After(s.now()) {
			k.Status = "expired"
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Service) Authenticate(ctx context.Context, plaintext, remoteIP string) (Identity, error) {
	id, secret, err := ParseKey(plaintext)
	if err != nil {
		return Identity{}, ErrInvalidKey
	}
	digest := sha256.Sum256(secret)
	var stored []byte
	var identity Identity
	var enabled bool
	var expires, revoked *time.Time
	err = s.pool.QueryRow(ctx, `SELECT k.secret_hash,k.client_id,c.name,k.scopes,c.enabled,k.expires_at,k.revoked_at FROM api_keys k JOIN api_clients c ON c.id=k.client_id WHERE k.id=$1`, id).Scan(&stored, &identity.ClientID, &identity.ClientName, &identity.Scopes, &enabled, &expires, &revoked)
	if err != nil || len(stored) != sha256.Size || subtle.ConstantTimeCompare(digest[:], stored) != 1 || !enabled || revoked != nil || (expires != nil && !expires.After(s.now())) {
		return Identity{}, ErrInvalidKey
	}
	identity.KeyID = id
	if ip := net.ParseIP(remoteIP); ip != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now(),last_used_ip=$2 WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now()-$3::interval)`, id, ip.String(), s.usageInterval.String())
	} else {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at < now()-$2::interval)`, id, s.usageInterval.String())
	}
	return identity, nil
}

func audit(ctx context.Context, tx pgx.Tx, clientID string, keyID *string, action, actor, details string) error {
	_, err := tx.Exec(ctx, `INSERT INTO api_credential_audit_events(id,client_id,key_id,action,actor,details) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, ulid.Make().String(), clientID, keyID, action, actor, details)
	return err
}
