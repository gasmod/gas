package apikey

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/apikeystore"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
)

// metadataReason is the reason ErrInvalidMetadata gives in place of the
// encoding/json error, which can quote a value.
const metadataReason = "does not encode as JSON"

// displayLength is how many characters of a key's random part KeyPrefix
// shows after Settings.Prefix.
const displayLength = 8

// KeyInfo describes a stored API key, as described in the package
// documentation. It holds neither the key nor its hash.
type KeyInfo struct {
	// CreatedAt is when the key was generated.
	CreatedAt time.Time
	// LastUsed is when the key last authenticated a request, or nil if
	// it never has.
	LastUsed *time.Time
	// ExpiresAt is when the key stops authenticating, or nil if it never
	// expires.
	ExpiresAt *time.Time
	// DeletedAt is when the key was revoked, or nil if it is not.
	DeletedAt *time.Time
	// Metadata holds the values given to WithMetadata, as decoded from
	// JSON. It is never nil.
	Metadata map[string]any
	// ID identifies the key. It is the CredentialID of the principals the
	// key authenticates.
	ID string
	// Subject is the user or client the key belongs to.
	Subject string
	// Name is the label given to Generate.
	Name string
	// KeyPrefix is Settings.Prefix and the first eight characters of the
	// key's random part, to tell keys apart.
	KeyPrefix string
	// Scopes are the scopes given to Generate. It is never nil.
	Scopes []string
}

// GenerateOption customizes a call to Service.Generate.
type GenerateOption func(*generateOptions)

// expiryKind is which expiry option was given last.
type expiryKind int

const (
	// noExpiry means the key never expires.
	noExpiry expiryKind = iota
	// expiryTTL means the key expires a TTL after it is generated.
	expiryTTL
	// expiryAt means the key expires at a given time.
	expiryAt
)

// generateOptions holds what the GenerateOptions recorded.
type generateOptions struct {
	// metadata is the map given to WithMetadata.
	metadata map[string]any
	// expiresAt is the time given to WithExpiresAt.
	expiresAt time.Time
	// ttl is the duration given to WithTTL.
	ttl time.Duration
	// expiry is which of ttl and expiresAt applies.
	expiry expiryKind
}

// WithMetadata stores md with the key, as JSON. Authenticate puts its
// values in the principal's metadata. md may not hold the key
// PrincipalMetadataKeyScopes. A nil or empty md stores no metadata.
func WithMetadata(md map[string]any) GenerateOption {
	return func(o *generateOptions) { o.metadata = md }
}

// WithTTL makes the key expire ttl after Generate is called. A ttl that is
// not positive makes Generate fail with ErrInvalidTTL. Of WithTTL and
// WithExpiresAt, the last one given applies.
//
// Default: the key never expires.
func WithTTL(ttl time.Duration) GenerateOption {
	return func(o *generateOptions) {
		o.ttl = ttl
		o.expiry = expiryTTL
	}
}

// WithExpiresAt makes the key expire at t. A t that is not after the time
// Generate is called, the zero time included, makes Generate fail with
// ErrInvalidExpiry. Of WithTTL and WithExpiresAt, the last one given
// applies.
//
// Default: the key never expires.
func WithExpiresAt(t time.Time) GenerateOption {
	return func(o *generateOptions) {
		o.expiresAt = t
		o.expiry = expiryAt
	}
}

// expiresAtFrom returns the expiry the options ask for, given the current
// time, or nil when the key never expires.
func (o *generateOptions) expiresAtFrom(now time.Time) (*time.Time, error) {
	switch o.expiry {
	case expiryTTL:
		if o.ttl <= 0 {
			return nil, fmt.Errorf("%s: %w: must be positive", ServiceName, ErrInvalidTTL)
		}

		return new(now.Add(o.ttl)), nil
	case expiryAt:
		if !o.expiresAt.After(now) {
			return nil, fmt.Errorf("%s: %w: must be in the future", ServiceName, ErrInvalidExpiry)
		}

		return new(o.expiresAt), nil
	case noExpiry:
	}

	return nil, nil
}

// Generate creates a key for subject, labeled name, with scopes, and
// stores its hash, as described in the package documentation. It returns
// the key, which is not stored and cannot be read again, and the stored
// key's KeyInfo. An invalid subject, name, expiry, scope or metadata
// fails before anything is stored.
func (s *Service) Generate(
	ctx context.Context,
	subject, name string,
	scopes []string,
	opts ...GenerateOption,
) (key string, info *KeyInfo, err error) {
	st, err := s.current()
	if err != nil {
		return "", nil, err
	}

	err = checkSubjectAndName(subject, name)
	if err != nil {
		return "", nil, err
	}

	var o generateOptions

	for _, opt := range opts {
		opt(&o)
	}

	createdAt := time.Now()

	expiresAt, err := o.expiresAtFrom(createdAt)
	if err != nil {
		return "", nil, err
	}

	scopes, err = checkScopes(scopes)
	if err != nil {
		return "", nil, err
	}

	metadataJSON, metadata, err := encodeMetadata(o.metadata)
	if err != nil {
		return "", nil, err
	}

	key, display, err := newKey(&st.cfg.APIKey)
	if err != nil {
		return "", nil, err
	}

	info = &KeyInfo{
		ID:        uuid.NewString(),
		Subject:   subject,
		Name:      name,
		KeyPrefix: display,
		Scopes:    scopes,
		Metadata:  metadata,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
	}

	err = st.store.InsertKey(ctx, insertParams(info, cryptoutil.SHA256Hex(key), metadataJSON))
	if err != nil {
		return "", nil, fmt.Errorf("%s: generate key: %w", ServiceName, err)
	}

	return key, info, nil
}

// checkSubjectAndName returns an error wrapping ErrEmptySubject for an
// empty subject, or ErrValueTooLong for a subject or name longer than
// auth.MaxValueLength bytes, naming the field and never the value.
func checkSubjectAndName(subject, name string) error {
	if subject == "" {
		return fmt.Errorf("%s: %w", ServiceName, ErrEmptySubject)
	}

	err := auth.CheckLength("subject", subject)
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	err = auth.CheckLength("name", name)
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	return nil
}

// insertParams returns the values to store a new key with: its KeyInfo,
// its hash, and its metadata as JSON.
func insertParams(info *KeyInfo, keyHash string, metadataJSON []byte) apikeystore.InsertKeyParams {
	return apikeystore.InsertKeyParams{
		ID:        info.ID,
		Subject:   info.Subject,
		Name:      info.Name,
		KeyHash:   keyHash,
		KeyPrefix: info.KeyPrefix,
		Scopes:    info.Scopes,
		Metadata:  metadataJSON,
		ExpiresAt: info.ExpiresAt,
		CreatedAt: info.CreatedAt,
	}
}

// newKey returns a new key made as the settings say, and its display
// prefix: the settings' prefix and the first displayLength characters of
// the random part. KeyLength is at least 16 bytes, which encode to 22
// characters, so the random part is long enough.
func newKey(settings *Settings) (key, display string, err error) {
	random, err := cryptoutil.RandomString(settings.KeyLength)
	if err != nil {
		return "", "", fmt.Errorf("%s: generate key: %w", ServiceName, err)
	}

	return settings.Prefix + random, settings.Prefix + random[:displayLength], nil
}

// checkScopes returns a copy of scopes, empty rather than nil, or an
// error wrapping ErrInvalidScope for the first scope that is empty, holds
// a comma, or starts or ends with white space.
func checkScopes(scopes []string) ([]string, error) {
	for _, scope := range scopes {
		var reason string

		switch {
		case scope == "":
			reason = "must not be empty"
		case strings.Contains(scope, ","):
			reason = "must not contain a comma"
		case strings.TrimFunc(scope, unicode.IsSpace) != scope:
			reason = "must not start or end with white space"
		default:
			continue
		}

		return nil, fmt.Errorf("%s: %w %q: %s", ServiceName, ErrInvalidScope, scope, reason)
	}

	if scopes == nil {
		return []string{}, nil
	}

	return slices.Clone(scopes), nil
}

// encodeMetadata returns md as the JSON to store and as the map that JSON
// decodes to, which is what List and Authenticate later return. It
// refuses the reserved key PrincipalMetadataKeyScopes. Metadata that does
// not encode is refused with a fixed reason: encoding/json's errors can
// quote a value, so they are replaced, not wrapped.
func encodeMetadata(md map[string]any) ([]byte, map[string]any, error) {
	_, reserved := md[PrincipalMetadataKeyScopes]
	if reserved {
		return nil, nil, fmt.Errorf(
			"%s: %w %q", ServiceName, ErrReservedMetadataKey, PrincipalMetadataKeyScopes,
		)
	}

	if len(md) == 0 {
		return []byte("{}"), map[string]any{}, nil
	}

	b, err := json.Marshal(md)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w: %s", ServiceName, ErrInvalidMetadata, metadataReason)
	}

	var decoded map[string]any

	err = json.Unmarshal(b, &decoded)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w: %s", ServiceName, ErrInvalidMetadata, metadataReason)
	}

	return b, decoded, nil
}

// ListOption customizes a call to Service.List.
type ListOption func(*listOptions)

// listOptions holds what the ListOptions recorded.
type listOptions struct {
	// includeRevoked lists revoked keys too.
	includeRevoked bool
}

// WithIncludeRevoked makes List return revoked keys too, with their
// KeyInfo.DeletedAt set.
//
// Default: revoked keys are left out.
func WithIncludeRevoked() ListOption {
	return func(o *listOptions) { o.includeRevoked = true }
}

// List returns the keys of subject, newest first, keys created at the same
// stored time ordered by ID, as described in the package documentation. Revoked keys are left out unless
// WithIncludeRevoked is given; expired keys are listed until they are
// deleted. A subject without keys gets an empty slice.
func (s *Service) List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error) {
	st, err := s.current()
	if err != nil {
		return nil, err
	}

	var o listOptions

	for _, opt := range opts {
		opt(&o)
	}

	keys, err := st.store.ListKeysBySubject(ctx, subject, o.includeRevoked)
	if err != nil {
		return nil, fmt.Errorf("%s: list keys: %w", ServiceName, err)
	}

	out := make([]KeyInfo, len(keys))
	for i := range keys {
		out[i] = KeyInfo(keys[i])
	}

	return out, nil
}

// Revoke revokes the key principal authenticated with, by its
// CredentialID: the key stays stored, and authenticating with it fails
// with auth.ErrCredentialRevoked. An unknown key and a key already
// revoked are no-ops. A nil principal is ErrNilPrincipal, and a principal
// of another scheme ErrSchemeMismatch.
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	id, err := apiKeyCredential(principal)
	if err != nil {
		return err
	}

	err = st.store.SoftDeleteKeyByID(ctx, id, time.Now())
	if err != nil {
		return fmt.Errorf("%s: revoke: %w", ServiceName, err)
	}

	return nil
}

// RevokeAll revokes every key of subject, as Revoke does one. Keys
// already revoked keep their revocation time.
func (s *Service) RevokeAll(ctx context.Context, subject string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	err = st.store.SoftDeleteKeysBySubject(ctx, subject, time.Now())
	if err != nil {
		return fmt.Errorf("%s: revoke all: %w", ServiceName, err)
	}

	return nil
}

// RevokeAllByScheme is RevokeAll when scheme is auth.SchemeAPIKey, and a
// no-op for any other scheme. Either way it fails with ErrNotInitialized
// when the service is not initialized.
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	_, err := s.current()
	if err != nil {
		return err
	}

	if scheme != auth.SchemeAPIKey {
		return nil
	}

	return s.RevokeAll(ctx, subject)
}

// Delete removes the stored key principal authenticated with, by its
// CredentialID, revoked or not. Authenticating with it then fails with
// auth.ErrUnauthenticated, as for a key that never existed. An unknown key
// is a no-op. A nil principal is ErrNilPrincipal, and a principal of
// another scheme ErrSchemeMismatch.
func (s *Service) Delete(ctx context.Context, principal gas.Principal) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	id, err := apiKeyCredential(principal)
	if err != nil {
		return err
	}

	err = st.store.HardDeleteKeyByID(ctx, id)
	if err != nil {
		return fmt.Errorf("%s: delete: %w", ServiceName, err)
	}

	return nil
}

// DeleteAll removes every stored key of subject, as Delete does one.
func (s *Service) DeleteAll(ctx context.Context, subject string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	err = st.store.HardDeleteKeysBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("%s: delete all: %w", ServiceName, err)
	}

	return nil
}

// apiKeyCredential returns the key ID of an API key principal, an error
// wrapping ErrNilPrincipal when principal is nil or a nil pointer, and one
// wrapping ErrSchemeMismatch when it is of another scheme.
func apiKeyCredential(principal gas.Principal) (string, error) {
	if principal == nil {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrNilPrincipal)
	}

	v := reflect.ValueOf(principal)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrNilPrincipal)
	}

	if principal.Scheme() != auth.SchemeAPIKey {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrSchemeMismatch)
	}

	return principal.CredentialID(), nil
}
