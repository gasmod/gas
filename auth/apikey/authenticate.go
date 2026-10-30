package apikey

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/apikeystore"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
)

// Authenticate reads the key from the configured header and returns the
// principal of the key it names, as described in the package
// documentation. A missing key is ErrNoCredentials; an unknown, revoked
// or expired one matches auth.ErrUnauthenticated. No error includes the
// key.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	st, err := s.current()
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrNoCredentials)
	}

	key := r.Header.Get(st.cfg.APIKey.HeaderName)
	if key == "" {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrNoCredentials)
	}

	rec, err := st.store.GetKeyByHash(ctx, cryptoutil.SHA256Hex(key))
	if errors.Is(err, apikeystore.ErrNotFound) {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrUnauthenticated)
	}

	if err != nil {
		return nil, fmt.Errorf("%s: authenticate: %w", ServiceName, err)
	}

	if rec.DeletedAt != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrCredentialRevoked)
	}

	now := time.Now()

	if rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt) {
		return nil, fmt.Errorf("%s: %w", ServiceName, auth.ErrCredentialsExpired)
	}

	s.touch(ctx, st.store, rec.ID, now)

	meta := gas.BasePrincipalMetadata(rec.Metadata)

	meta[PrincipalMetadataKeyScopes] = rec.Scopes

	return auth.NewPrincipal(rec.Subject, auth.SchemeAPIKey, rec.ID, meta), nil
}

// touch records now as the key's last use. It is best-effort: a failure
// is logged at warn level and does not fail authentication, unless the
// request's context has ended, in which case it is not logged either.
func (s *Service) touch(ctx context.Context, store *apikeystore.Store, id string, now time.Time) {
	err := store.UpdateLastUsed(ctx, id, now)
	if err == nil || ctx.Err() != nil {
		return
	}

	s.logger.Warn("api key last used update failed").
		Str("service", ServiceName).
		Str("key_id", id).
		Err("error", err).
		Send()
}
