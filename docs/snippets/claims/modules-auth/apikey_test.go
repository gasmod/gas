package modulesauth_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
)

// Docs: modules/auth.mdx#api-keys
// "`Generate(ctx, subject, name, scopes, opts...)` creates a key of
// `apikey.keylength` random bytes, URL-safe base64, after `apikey.prefix`. It
// returns the full key **exactly once**, with a `*apikey.KeyInfo`; only the
// key's SHA-256 hash is stored, along with a display prefix (`apikey.prefix`
// plus the first 8 characters of the random part). A scope containing a comma
// is rejected."
func TestAPIKeyGenerate(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	cfg := apikey.DefaultConfig()
	cfg.APIKey.Prefix = "sk_live_"
	cfg.APIKey.KeyLength = 24
	svc := mustAPIKey(t, db, cfg)
	key, info := generate(t, svc, "svc-1", []string{"read", "write"})
	random, ok := strings.CutPrefix(key, "sk_live_")
	raw, err := base64.RawURLEncoding.DecodeString(random)
	if !ok || err != nil || len(raw) != 24 {
		t.Errorf("key %q: prefix %v, %d random bytes (%v), want sk_live_ and 24", key, ok, len(raw), err)
	}
	sum := sha256.Sum256([]byte(key))
	if info.KeyHash != hex.EncodeToString(sum[:]) {
		t.Errorf("KeyHash = %q, want the SHA-256 of the full key", info.KeyHash)
	}
	if info.KeyPrefix != "sk_live_"+random[:8] {
		t.Errorf("KeyPrefix = %q, want sk_live_ plus 8 characters", info.KeyPrefix)
	}

	// No stored column holds the key or its random part.
	rows, err := db.DB().QueryContext(t.Context(), "SELECT * FROM __gas_auth_api_keys")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for i, v := range vals {
			if strings.Contains(v.String, random) {
				t.Errorf("column %s holds the key", cols[i])
			}
		}
	}
	listed, err := svc.List(t.Context(), "svc-1")
	if err != nil || len(listed) != 1 || listed[0].KeyHash != info.KeyHash {
		t.Errorf("List = %+v, %v", listed, err)
	}

	if _, _, err := svc.Generate(t.Context(), "svc-1", "bad", []string{"a,b"}); err == nil {
		t.Error("a scope with a comma was accepted")
	}
}

// Docs: modules/auth.mdx#api-keys
// "`WithTTL(d)` | Expires the key `d` from now" and "`WithExpiresAt(t)` |
// Expires the key at `t`" and "`WithMetadata(m)` | Stores metadata returned
// with the principal" and "A non-positive duration or zero time is ignored,
// and when both expiry options are given the last one wins."
func TestAPIKeyOptions(t *testing.T) {
	t.Parallel()
	svc := mustAPIKey(t, sqliteDB(t), nil)
	at := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	cases := map[string]struct {
		opts []apikey.GenerateOption
		want func(*time.Time) bool
	}{
		"none":         {nil, func(e *time.Time) bool { return e == nil }},
		"ttl":          {[]apikey.GenerateOption{apikey.WithTTL(time.Hour)}, func(e *time.Time) bool { return e != nil && time.Until(*e) > 59*time.Minute }},
		"zero ttl":     {[]apikey.GenerateOption{apikey.WithTTL(0)}, func(e *time.Time) bool { return e == nil }},
		"negative ttl": {[]apikey.GenerateOption{apikey.WithTTL(-time.Hour)}, func(e *time.Time) bool { return e == nil }},
		"zero time":    {[]apikey.GenerateOption{apikey.WithExpiresAt(time.Time{})}, func(e *time.Time) bool { return e == nil }},
		"at":           {[]apikey.GenerateOption{apikey.WithExpiresAt(at)}, func(e *time.Time) bool { return e != nil && e.Equal(at) }},
		"ttl then at":  {[]apikey.GenerateOption{apikey.WithTTL(time.Hour), apikey.WithExpiresAt(at)}, func(e *time.Time) bool { return e != nil && e.Equal(at) }},
		"at then ttl":  {[]apikey.GenerateOption{apikey.WithExpiresAt(at), apikey.WithTTL(time.Hour)}, func(e *time.Time) bool { return e != nil && time.Until(*e) < 2*time.Hour }},
		"at then zero": {[]apikey.GenerateOption{apikey.WithExpiresAt(at), apikey.WithTTL(0)}, func(e *time.Time) bool { return e != nil && e.Equal(at) }},
	}
	for name, tc := range cases {
		_, info := generate(t, svc, "s", nil, tc.opts...)
		if !tc.want(info.ExpiresAt) {
			t.Errorf("%s: ExpiresAt = %v", name, info.ExpiresAt)
		}
	}
	key, _ := generate(t, svc, "s", []string{"read"}, apikey.WithMetadata(map[string]any{"team": "ops"}))
	p, err := svc.Authenticate(t.Context(), apiKeyRequest(key))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if v, ok := gas.MetadataValue[string](p.Metadata(), "team"); !ok || v != "ops" {
		t.Errorf("metadata team = %q, %v", v, ok)
	}
}

// Docs: modules/auth.mdx#api-keys
// "`Authenticate` reads the key from the `apikey.headername` header
// (`X-API-Key` by default). A missing, unknown or revoked key fails with
// `auth.ErrUnauthenticated`, an expired one with `auth.ErrCredentialsExpired`.
// On success it records the time as the key's last use. The principal's
// credential ID is the key's ID, and its metadata holds the stored metadata
// plus the key's scopes as a `[]string` under `scopes`
// (`apikey.PrincipalMetadataKeyScopes`)."
func TestAPIKeyAuthenticate(t *testing.T) {
	t.Parallel()
	if apikey.DefaultConfig().APIKey.HeaderName != "X-API-Key" || apikey.PrincipalMetadataKeyScopes != "scopes" {
		t.Error("default header or scopes key differs")
	}
	db := sqliteDB(t)
	cfg := apikey.DefaultConfig()
	cfg.APIKey.HeaderName = "X-Machine-Key"
	svc := mustAPIKey(t, db, cfg)
	ctx := t.Context()
	key, info := generate(t, svc, "svc", []string{"read", "write"})
	if _, err := svc.Authenticate(ctx, apiKeyRequest(key)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("key in the default header with a custom headername: %v", err)
	}
	before := time.Now().Add(-time.Second)
	p, err := svc.Authenticate(ctx, keyRequest("X-Machine-Key", key))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Subject() != "svc" || p.CredentialID() != info.ID {
		t.Errorf("principal = %v", p)
	}
	if scopes, ok := gas.MetadataValue[[]string](p.Metadata(), apikey.PrincipalMetadataKeyScopes); !ok || !slices.Equal(scopes, []string{"read", "write"}) {
		t.Errorf("scopes metadata = %v, %v", scopes, ok)
	}
	listed, _ := svc.List(ctx, "svc")
	if len(listed) != 1 || listed[0].LastUsed == nil || listed[0].LastUsed.Before(before) {
		t.Errorf("LastUsed = %v, want the authentication time", listed[0].LastUsed)
	}

	expiredKey, _ := generate(t, svc, "svc", nil, apikey.WithExpiresAt(time.Now().Add(-time.Hour)))
	revokedKey, _ := generate(t, svc, "other", nil)
	rp, _ := svc.Authenticate(ctx, keyRequest("X-Machine-Key", revokedKey))
	if err := svc.Revoke(ctx, rp); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for name, k := range map[string]string{"missing": "", "unknown": "sk_nope", "revoked": revokedKey} {
		if _, err := svc.Authenticate(ctx, keyRequest("X-Machine-Key", k)); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s key: %v, want ErrUnauthenticated", name, err)
		}
	}
	if _, err := svc.Authenticate(ctx, keyRequest("X-Machine-Key", expiredKey)); !errors.Is(err, auth.ErrCredentialsExpired) {
		t.Errorf("expired key: %v, want ErrCredentialsExpired", err)
	}
}

// Docs: modules/auth.mdx#api-keys
// "`Revoke`, `RevokeAll` and `RevokeAllByScheme` soft-delete: the row keeps a
// `deleted_at` time and is excluded from authentication and from `List`.
// `Delete` and `DeleteAll` remove rows permanently. `List(ctx, subject)`
// returns a subject's active keys; with `apikey.WithIncludeRevoked()` it also
// returns revoked ones, whose `DeletedAt` is set."
func TestAPIKeyRevokeAndDelete(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	svc := mustAPIKey(t, db, nil)
	ctx := t.Context()
	principalFor := func(key string) gas.Principal {
		p, err := svc.Authenticate(ctx, apiKeyRequest(key))
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		return p
	}
	k1, _ := generate(t, svc, "u", nil)
	k2, _ := generate(t, svc, "u", nil)
	k3, _ := generate(t, svc, "u", nil)
	if err := svc.Revoke(ctx, principalFor(k1)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	active, _ := svc.List(ctx, "u")
	all, _ := svc.List(ctx, "u", apikey.WithIncludeRevoked())
	if len(active) != 2 || len(all) != 3 {
		t.Errorf("List = %d active, %d with revoked; want 2 and 3", len(active), len(all))
	}
	revoked := 0
	for _, k := range all {
		if k.DeletedAt != nil {
			revoked++
		}
	}
	if revoked != 1 {
		t.Errorf("keys with DeletedAt = %d, want 1", revoked)
	}
	var deletedAt sql.NullString
	if err := db.DB().QueryRowContext(ctx, "SELECT deleted_at FROM __gas_auth_api_keys WHERE deleted_at IS NOT NULL").Scan(&deletedAt); err != nil || !deletedAt.Valid {
		t.Errorf("revoked row deleted_at = %v, %v", deletedAt, err)
	}

	if err := svc.RevokeAllByScheme(ctx, "u", auth.SchemeSession); err != nil {
		t.Fatalf("RevokeAllByScheme(session): %v", err)
	}
	if active, _ := svc.List(ctx, "u"); len(active) != 2 {
		t.Errorf("RevokeAllByScheme with another scheme revoked keys: %d active", len(active))
	}
	if err := svc.RevokeAllByScheme(ctx, "u", auth.SchemeAPIKey); err != nil {
		t.Fatalf("RevokeAllByScheme(apikey): %v", err)
	}
	if active, _ := svc.List(ctx, "u"); len(active) != 0 {
		t.Errorf("RevokeAllByScheme(apikey) left %d active", len(active))
	}
	if n := rowCount(t, db.DB(), "__gas_auth_api_keys"); n != 3 {
		t.Errorf("rows after revoking = %d, want 3 kept", n)
	}
	k4, _ := generate(t, svc, "v", nil)
	_, _ = generate(t, svc, "v", nil)
	if err := svc.RevokeAll(ctx, "v"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if _, err := svc.Authenticate(ctx, apiKeyRequest(k4)); err == nil {
		t.Error("RevokeAll left a key usable")
	}
	_ = k2
	_ = k3

	k5, _ := generate(t, svc, "w", nil)
	_, _ = generate(t, svc, "w", nil)
	if err := svc.Delete(ctx, principalFor(k5)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if all, _ := svc.List(ctx, "w", apikey.WithIncludeRevoked()); len(all) != 1 {
		t.Errorf("after Delete: %d keys with revoked, want 1", len(all))
	}
	if err := svc.DeleteAll(ctx, "u"); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if all, _ := svc.List(ctx, "u", apikey.WithIncludeRevoked()); len(all) != 0 {
		t.Errorf("after DeleteAll: %d keys, want 0", len(all))
	}
}

// Docs: modules/auth.mdx#api-keys
// "`WithTx(tx)` returns a provider whose operations run in your `*sql.Tx`, so a
// key can be created in the same transaction as your own writes."
func TestAPIKeyWithTx(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	svc := mustAPIKey(t, db, nil)
	ctx := t.Context()
	if _, err := db.Exec(ctx, "CREATE TABLE accounts (id TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, commit := range []bool{false, true} {
		var key string
		err := db.WithTx(ctx, nil, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO accounts (id) VALUES ('a')"); err != nil {
				return err
			}
			k, _, err := svc.WithTx(tx).Generate(ctx, "a", "ci", nil)
			key = k
			if err != nil {
				return err
			}
			if !commit {
				return errors.New("roll back")
			}
			return nil
		})
		_, authErr := svc.Authenticate(ctx, apiKeyRequest(key))
		accounts := rowCount(t, db.DB(), "accounts")
		if commit && (err != nil || authErr != nil || accounts != 1) {
			t.Errorf("committed: tx %v, key %v, accounts %d", err, authErr, accounts)
		}
		if !commit && (authErr == nil || accounts != 0) {
			t.Errorf("rolled back: key still works (%v) or accounts = %d", authErr, accounts)
		}
	}
}

// Docs: modules/auth.mdx#configuration
// "| `apikey.headername` | string | `X-API-Key` | Request header that carries the key. |"
// and "| `apikey.prefix` | string | empty | Prepended to generated keys, for example `sk_live_`. |"
// and "| `apikey.keylength` | int | `32` | Random bytes per key. At least 16. |"
// and "The minimums and "greater than zero" rules are `validate` tags, checked
// only when the config is bound."
func TestAPIKeyConfigTable(t *testing.T) {
	t.Parallel()
	d := apikey.DefaultConfig().APIKey
	if d.HeaderName != "X-API-Key" || d.Prefix != "" || d.KeyLength != 32 {
		t.Errorf("defaults = %+v", d)
	}
	svc, err := startStore(t, "apikey", sqliteDB(t), boundConfig(t, map[string]any{"apikey": map[string]any{
		"headername": "X-Bound", "prefix": "pk_", "keylength": 16,
	}}), nil, nil)
	if err != nil {
		t.Fatalf("bound Init: %v", err)
	}
	s := svc.(*apikey.Service)
	key, _ := generate(t, s, "u", nil)
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "pk_"))
	if !strings.HasPrefix(key, "pk_") || len(raw) != 16 {
		t.Errorf("bound key %q: want pk_ and 16 bytes", key)
	}
	if _, err := s.Authenticate(t.Context(), keyRequest("X-Bound", key)); err != nil {
		t.Errorf("bound header name: %v", err)
	}
	if _, err := startStore(t, "apikey", sqliteDB(t), boundConfig(t, map[string]any{"apikey": map[string]any{"keylength": 15}}), nil, nil); err == nil {
		t.Error("bound keylength 15: Init succeeded, want a validation error")
	}
	given := apikey.DefaultConfig()
	given.APIKey.KeyLength = 8
	g, err := startStore(t, "apikey", sqliteDB(t), nil, nil, apikey.WithConfig(given))
	if err != nil {
		t.Fatalf("WithConfig keylength 8: %v, want the tag skipped", err)
	}
	k, _ := generate(t, g.(*apikey.Service), "u", nil)
	if raw, _ := base64.RawURLEncoding.DecodeString(k); len(raw) != 8 {
		t.Errorf("WithConfig keylength 8 gave %d bytes", len(raw))
	}
}

// Docs: modules/auth.mdx#configuration
// "`apikey.keylength` below 6 bytes, possible only through `WithConfig`, makes
// `Generate` panic instead of returning an error."
func TestAPIKeyShortLengthPanics(t *testing.T) {
	t.Parallel()
	for n, wantPanic := range map[int]bool{5: true, 1: true, 6: false} {
		cfg := apikey.DefaultConfig()
		cfg.APIKey.KeyLength = n
		svc := mustAPIKey(t, sqliteDB(t), cfg)
		panicked := false
		func() {
			defer func() { panicked = recover() != nil }()
			_, _, _ = svc.Generate(t.Context(), "u", "n", nil)
		}()
		if panicked != wantPanic {
			t.Errorf("keylength %d: Generate panicked = %v, want %v", n, panicked, wantPanic)
		}
	}
}
