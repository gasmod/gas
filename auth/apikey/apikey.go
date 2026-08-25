// Package apikey provides API key authentication for the Gas ecosystem.
// It implements gas.Authenticator, gas.PrincipalRevoker, and gas.Service.
package apikey

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey/db"
	"github.com/gasmod/gas/auth/internal/cryptoutil"

	"github.com/google/uuid"
)

const serviceName = "gas/auth/apikey"

// KeyInfo contains non-sensitive information about an API key.
// Re-exported from the db package for convenience.
type KeyInfo = db.KeyInfo

// Service is an API key authenticator implementing gas.Authenticator,
// gas.PrincipalRevoker, and gas.Service.
type Service struct {
	store  *db.Store
	logger gas.Logger
	cfg    *Config

	cfgProvider gas.ConfigProvider

	customConfigProvided bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.Authenticator = (*Service)(nil)
var _ gas.PrincipalRevoker = (*Service)(nil)
var _ Provider = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithConfig sets a custom configuration, skipping auto-bind from ConfigProvider.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// New captures options and returns a DI-injectable constructor.
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) *Service {
	return func(dbProv gas.DatabaseProvider, logger gas.Logger, migMgr gas.MigrationManager, cfgProvider gas.ConfigProvider) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			store:       db.New()(dbProv, logger, migMgr),
			logger:      logger.With().Str("service", serviceName).Logger(),
			cfgProvider: cfgProvider,
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// Init validates configuration and initializes the store.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	if err := s.cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", s.Name(), err)
	}

	if err := s.store.Init(s.Name()); err != nil {
		return fmt.Errorf("%s: store init: %w", s.Name(), err)
	}

	s.logger.Info("service initialized").Send()
	return nil
}

// Close performs cleanup. API key service is stateless so this is a no-op.
func (s *Service) Close() error { return nil }

// Authenticate reads the API key from the configured header, hashes it,
// looks it up in the database, validates expiry, updates last_used, and
// returns a principal with scheme "apikey".
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	key := r.Header.Get(s.cfg.APIKey.HeaderName)
	if key == "" {
		return nil, auth.ErrUnauthenticated
	}

	keyHash := cryptoutil.SHA256Hex(key)

	rec, err := s.store.GetKeyByHash(ctx, keyHash)
	if err != nil {
		return nil, fmt.Errorf("%s: authenticate: %w", s.Name(), err)
	}

	if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
		return nil, auth.ErrCredentialsExpired
	}

	// Update last_used.
	if upErr := s.store.UpdateLastUsed(ctx, rec.ID, time.Now()); upErr != nil {
		return nil, fmt.Errorf("%s: update last used: %w", s.Name(), upErr)
	}

	meta := gas.BasePrincipalMetadata(rec.Metadata)
	meta[PrincipalMetadataKeyScopes] = rec.Scopes

	return auth.NewPrincipal(rec.Subject, auth.SchemeAPIKey, rec.ID, meta), nil
}

// Revoke soft-deletes an API key by the principal's credential ID. The row
// remains in the database with deleted_at set, but is excluded from
// authentication and listing. Use Delete for permanent removal.
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error {
	if err := s.store.SoftDeleteKeyByID(ctx, principal.CredentialID(), time.Now()); err != nil {
		return fmt.Errorf("%s: revoke: %w", s.Name(), err)
	}
	return nil
}

// RevokeAll soft-deletes all API keys for the given subject. Use DeleteAll
// for permanent removal.
func (s *Service) RevokeAll(ctx context.Context, subject string) error {
	if err := s.store.SoftDeleteKeysBySubject(ctx, subject, time.Now()); err != nil {
		return fmt.Errorf("%s: revoke all: %w", s.Name(), err)
	}
	return nil
}

// Delete permanently removes an API key row by the principal's credential ID.
// Prefer Revoke for normal flows; use Delete only when the row must be purged
// (e.g. GDPR erasure, test cleanup).
func (s *Service) Delete(ctx context.Context, principal gas.Principal) error {
	if err := s.store.HardDeleteKeyByID(ctx, principal.CredentialID()); err != nil {
		return fmt.Errorf("%s: delete: %w", s.Name(), err)
	}
	return nil
}

// DeleteAll permanently removes all API key rows for the given subject.
// Prefer RevokeAll for normal flows.
func (s *Service) DeleteAll(ctx context.Context, subject string) error {
	if err := s.store.HardDeleteKeysBySubject(ctx, subject); err != nil {
		return fmt.Errorf("%s: delete all: %w", s.Name(), err)
	}
	return nil
}

// RevokeAllByScheme delegates to RevokeAll if the scheme is "apikey",
// otherwise it is a no-op.
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	if scheme != auth.SchemeAPIKey {
		return nil
	}
	return s.RevokeAll(ctx, subject)
}

// GenerateOption customizes a call to Service.Generate.
type GenerateOption func(*generateOptions)

type generateOptions struct {
	expiresAt *time.Time
	metadata  map[string]any
}

// WithMetadata attaches a metadata map to the API key. It is stored as JSON
// and returned when the key is authenticated. Passing a nil or empty map is
// equivalent to not calling this option.
func WithMetadata(md map[string]any) GenerateOption {
	return func(o *generateOptions) { o.metadata = md }
}

// WithTTL sets the API key to expire after the given duration from now.
// A non-positive duration is ignored. If both WithTTL and WithExpiresAt are
// supplied, the last one wins.
func WithTTL(ttl time.Duration) GenerateOption {
	return func(o *generateOptions) {
		if ttl <= 0 {
			return
		}
		o.expiresAt = new(time.Now().Add(ttl))
	}
}

// WithExpiresAt sets an explicit expiration time on the API key. A zero time
// is ignored. If both WithTTL and WithExpiresAt are supplied, the last one wins.
func WithExpiresAt(t time.Time) GenerateOption {
	return func(o *generateOptions) {
		if t.IsZero() {
			return
		}
		o.expiresAt = new(t)
	}
}

// Generate creates a new API key for the given subject, stores its hash in
// the database, and returns the full key exactly once along with its record ID.
func (s *Service) Generate(ctx context.Context, subject, name string, scopes []string, opts ...GenerateOption) (key string, info *KeyInfo, err error) {
	var o generateOptions
	for _, opt := range opts {
		opt(&o)
	}

	rawKey, randErr := cryptoutil.RandomString(s.cfg.APIKey.KeyLength)
	if randErr != nil {
		return "", nil, fmt.Errorf("%s: generate key: %w", s.Name(), randErr)
	}

	fullKey := s.cfg.APIKey.Prefix + rawKey
	keyHash := cryptoutil.SHA256Hex(fullKey)
	keyPrefix := fullKey[:len(s.cfg.APIKey.Prefix)+8]

	id := uuid.New().String()

	if scopes == nil {
		scopes = []string{}
	}

	for _, scope := range scopes {
		if strings.Contains(scope, ",") {
			return "", nil, fmt.Errorf("%s: scope %q must not contain commas", s.Name(), scope)
		}
	}

	metadataJSON := []byte("{}")
	if len(o.metadata) > 0 {
		b, jsErr := json.Marshal(o.metadata)
		if jsErr != nil {
			return "", nil, fmt.Errorf("%s: marshal metadata: %w", s.Name(), jsErr)
		}
		metadataJSON = b
	}

	createdAt := time.Now()
	if insErr := s.store.InsertKey(ctx, db.InsertKeyParams{
		ID:        id,
		Subject:   subject,
		Name:      name,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
		Scopes:    scopes,
		Metadata:  metadataJSON,
		ExpiresAt: o.expiresAt,
		CreatedAt: createdAt,
	}); insErr != nil {
		return "", nil, fmt.Errorf("%s: generate key: %w", s.Name(), insErr)
	}

	metadataCopy := make(map[string]any, len(o.metadata))
	maps.Copy(metadataCopy, o.metadata)

	return fullKey, &KeyInfo{
		ID:        id,
		Subject:   subject,
		Name:      name,
		KeyHash:   keyHash,
		KeyPrefix: keyPrefix,
		Scopes:    scopes,
		Metadata:  metadataCopy,
		ExpiresAt: o.expiresAt,
		CreatedAt: createdAt,
	}, nil
}

// ListOption customizes a call to Service.List.
type ListOption func(*listOptions)

type listOptions struct {
	includeRevoked bool
}

// WithIncludeRevoked causes List to also return soft-deleted (revoked) keys.
// Revoked keys carry a non-nil KeyInfo.DeletedAt so callers can distinguish
// them from active keys.
func WithIncludeRevoked() ListOption {
	return func(o *listOptions) { o.includeRevoked = true }
}

// WithTx returns a Provider whose operations execute against tx. The caller
// owns tx lifecycle; the returned Provider is scoped to its lifetime and
// must not be cached.
func (s *Service) WithTx(tx *sql.Tx) Provider {
	cp := *s
	cp.store = s.store.WithTx(tx)
	return &cp
}

// List returns non-sensitive information about API keys for a subject. By
// default, only active (non-revoked) keys are returned; pass WithIncludeRevoked
// to also include soft-deleted keys.
func (s *Service) List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error) {
	var o listOptions
	for _, opt := range opts {
		opt(&o)
	}
	keys, err := s.store.ListKeysBySubject(ctx, subject, o.includeRevoked)
	if err != nil {
		return nil, fmt.Errorf("%s: list keys: %w", s.Name(), err)
	}
	return keys, nil
}
