package modulesauth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/jwt"
)

// Docs: modules/auth.mdx#jwt
// "`Authenticate` reads `Authorization: Bearer <token>`; the word `Bearer` is
// matched case-insensitively. A missing or malformed header, a bad signature, a
// token signed with another algorithm, or a wrong issuer or audience fails with
// `auth.ErrUnauthenticated`; an expired token fails with
// `auth.ErrCredentialsExpired`."
func TestJWTAuthenticate(t *testing.T) {
	t.Parallel()
	j := mustJWT(t, hsConfig())
	tok, err := j.Sign("u1", nil)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	for _, word := range []string{"Bearer", "bearer", "BEARER", "bEaReR"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", word+" "+tok)
		if p, err := j.Authenticate(t.Context(), r); err != nil || p.Subject() != "u1" {
			t.Errorf("%q scheme word: %v, %v", word, p, err)
		}
	}

	other := hsConfig()
	other.JWT.SigningKey = strings.Repeat("x", 40)
	otherTok, _ := mustJWT(t, other).Sign("u1", nil)
	hs384, err := gojwt.NewWithClaims(gojwt.SigningMethodHS384, gojwt.MapClaims{
		"sub": "u1", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(signingKey))
	if err != nil {
		t.Fatalf("sign HS384: %v", err)
	}
	issCfg := hsConfig()
	issCfg.JWT.Issuer, issCfg.JWT.Audience = "issuer-a", "aud-a"
	strict := mustJWT(t, issCfg)
	wrongIss := hsConfig()
	wrongIss.JWT.Issuer, wrongIss.JWT.Audience = "issuer-b", "aud-a"
	wrongAud := hsConfig()
	wrongAud.JWT.Issuer, wrongAud.JWT.Audience = "issuer-a", "aud-b"
	wrongIssTok, _ := mustJWT(t, wrongIss).Sign("u1", nil)
	wrongAudTok, _ := mustJWT(t, wrongAud).Sign("u1", nil)
	noIssTok := tok
	if _, err := strict.Authenticate(t.Context(), bearer(func() string { s, _ := strict.Sign("u1", nil); return s }())); err != nil {
		t.Fatalf("precondition: a token with the right issuer and audience failed: %v", err)
	}

	cases := map[string]struct {
		svc    *jwt.Service
		header string
	}{
		"missing":         {j, ""},
		"no scheme word":  {j, tok},
		"other scheme":    {j, "Token " + tok},
		"word only":       {j, "Bearer"},
		"garbage":         {j, "Bearer not.a.jwt"},
		"bad signature":   {j, "Bearer " + otherTok},
		"other algorithm": {j, "Bearer " + hs384},
		"wrong issuer":    {strict, "Bearer " + wrongIssTok},
		"wrong audience":  {strict, "Bearer " + wrongAudTok},
		"no issuer":       {strict, "Bearer " + noIssTok},
	}
	for name, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		if _, err := tc.svc.Authenticate(t.Context(), r); err != auth.ErrUnauthenticated { //nolint:errorlint // the sentinel itself
			t.Errorf("%s: err = %v, want auth.ErrUnauthenticated", name, err)
		}
	}
	expired, _ := j.SignWithExpiry("u1", nil, -time.Minute)
	if _, err := j.Authenticate(t.Context(), bearer(expired)); err != auth.ErrCredentialsExpired { //nolint:errorlint // the sentinel itself
		t.Errorf("expired: err = %v, want auth.ErrCredentialsExpired", err)
	}
}

// Docs: modules/auth.mdx#jwt
// "The principal's subject is the `sub` claim, its credential ID the `jti`
// claim, and its metadata every claim except `sub`, `jti`, `iss`, `aud`,
// `exp`, `iat` and `nbf`."
func TestJWTPrincipal(t *testing.T) {
	t.Parallel()
	cfg := hsConfig()
	cfg.JWT.Issuer, cfg.JWT.Audience = "iss-x", "aud-x"
	j := mustJWT(t, cfg)
	tok, err := j.Sign("u7", map[string]any{
		"jti": "id-7", "nbf": time.Now().Add(-time.Minute).Unix(), "role": "admin", "n": 3,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	p, err := j.Authenticate(t.Context(), bearer(tok))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Subject() != "u7" || p.CredentialID() != "id-7" {
		t.Errorf("subject, credential ID = %q, %q", p.Subject(), p.CredentialID())
	}
	meta := p.Metadata().(gas.BasePrincipalMetadata)
	if len(meta) != 2 || meta["role"] != "admin" || meta["n"] != float64(3) {
		t.Errorf("metadata = %v, want exactly role and n", meta)
	}
}

// Docs: modules/auth.mdx#jwt
// "`Sign(subject, claims)` signs a token that expires after `jwt.expiry`, and
// `SignWithExpiry` takes the lifetime explicitly. Both set `sub`, `iat` and
// `exp`, plus `iss` and `aud` when configured, after your claims, so your claims
// cannot override them. They add no `jti` of their own, so a principal from a
// token signed this way has an empty credential ID unless your claims include
// one. `Verify(token)` checks a token and returns its `*jwt.TokenClaims`
// without building a principal."
func TestJWTSign(t *testing.T) {
	t.Parallel()
	cfg := hsConfig()
	cfg.JWT.Expiry = 7 * time.Minute
	cfg.JWT.Issuer, cfg.JWT.Audience = "real-iss", "real-aud"
	j := mustJWT(t, cfg)
	evil := map[string]any{"sub": "evil", "iat": 1, "exp": 1, "iss": "evil", "aud": "evil"}

	tok, err := j.Sign("u1", evil)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	c, err := j.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Subject != "u1" || c.Issuer != "real-iss" || len(c.Audience) != 1 || c.Audience[0] != "real-aud" {
		t.Errorf("claims = %+v, want the service's sub, iss and aud", c)
	}
	if got := c.ExpiresAt.Sub(c.IssuedAt); got != 7*time.Minute {
		t.Errorf("Sign lifetime = %v, want jwt.expiry 7m", got)
	}
	if time.Since(c.IssuedAt) > time.Minute {
		t.Errorf("iat = %v, want now", c.IssuedAt)
	}
	if c.TokenID != "" {
		t.Errorf("jti = %q, want none added", c.TokenID)
	}
	tok2, _ := j.SignWithExpiry("u1", nil, 3*time.Minute)
	c2, err := j.Verify(tok2)
	if err != nil || c2.ExpiresAt.Sub(c2.IssuedAt) != 3*time.Minute {
		t.Errorf("SignWithExpiry lifetime = %v, %v; want 3m", c2.ExpiresAt.Sub(c2.IssuedAt), err)
	}
	if p, err := j.Authenticate(t.Context(), bearer(tok)); err != nil || p.CredentialID() != "" {
		t.Errorf("principal credential ID = %q, %v; want empty", p.CredentialID(), err)
	}

	// Without iss and aud configured, the caller's claims stay.
	plain := mustJWT(t, hsConfig())
	tok3, _ := plain.Sign("u1", map[string]any{"iss": "mine"})
	if c3, err := plain.Verify(tok3); err != nil || c3.Issuer != "mine" {
		t.Errorf("unconfigured issuer: claims %+v, %v; want the caller's iss kept", c3, err)
	}
}

// writeRSA writes an RSA key pair as PEM files and returns their paths.
func writeRSA(t *testing.T, pkcs1 bool) (pub, priv string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	var privBlock *pem.Block
	if pkcs1 {
		privBlock = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	} else {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("marshal private key: %v", err)
		}
		privBlock = &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	}
	dir := t.TempDir()
	pub, priv = filepath.Join(dir, "pub.pem"), filepath.Join(dir, "priv.pem")
	if err := os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv, pem.EncodeToMemory(privBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// Docs: modules/auth.mdx#jwt
// "HS256, the default, needs a signing key of at least 32 bytes. RS256 reads a
// PEM public key (PKIX) from `jwt.publickeypath` for verification and a PEM
// private key (PKCS #8 or PKCS #1) from `jwt.privatekeypath` for signing.
// `Init` requires a signing key either way, so even a service that only
// verifies RS256 tokens needs the private key path."
// and
// Docs: modules/auth.mdx#configuration
// "| `jwt.signingmethod` | string | `HS256` | `HS256` or `RS256`. |"
func TestJWTKeys(t *testing.T) {
	t.Parallel()
	if jwt.DefaultConfig().JWT.SigningMethod != "HS256" {
		t.Errorf("default signing method = %q", jwt.DefaultConfig().JWT.SigningMethod)
	}
	for n, ok := range map[int]bool{31: false, 32: true} {
		cfg := jwt.DefaultConfig()
		cfg.JWT.SigningKey = strings.Repeat("k", n)
		if _, err := newJWT(t, cfg); (err == nil) != ok {
			t.Errorf("HS256 key of %d bytes: Init err = %v, want ok %v", n, err, ok)
		}
	}
	for _, pkcs1 := range []bool{false, true} {
		pub, priv := writeRSA(t, pkcs1)
		cfg := jwt.DefaultConfig()
		cfg.JWT.SigningMethod = "RS256"
		cfg.JWT.PublicKeyPath, cfg.JWT.PrivateKeyPath = pub, priv
		svc, err := newJWT(t, cfg)
		if err != nil {
			t.Fatalf("RS256 Init (PKCS1 %v): %v", pkcs1, err)
		}
		tok, err := svc.Sign("u1", nil)
		if err != nil {
			t.Fatalf("RS256 Sign: %v", err)
		}
		parsed, _, err := gojwt.NewParser().ParseUnverified(tok, gojwt.MapClaims{})
		if err != nil || parsed.Method.Alg() != "RS256" {
			t.Errorf("token algorithm = %v, %v; want RS256", parsed, err)
		}
		if p, err := svc.Authenticate(t.Context(), bearer(tok)); err != nil || p.Subject() != "u1" {
			t.Errorf("RS256 Authenticate = %v, %v", p, err)
		}

		verifyOnly := jwt.DefaultConfig()
		verifyOnly.JWT.SigningMethod = "RS256"
		verifyOnly.JWT.PublicKeyPath = pub
		if _, err := newJWT(t, verifyOnly); err == nil || err.Error() != "jwt: signing key not configured" {
			t.Errorf("RS256 without a private key path: Init = %v, want jwt: signing key not configured", err)
		}
	}
}

// Docs: modules/auth.mdx#wiring
// "In `Init`, each service binds its section of the configuration and
// validates it (see Configuration), unless `WithConfig(cfg)` was passed: a
// config given that way replaces the bound one and is used as given, without
// its `validate` tags."
// and "a zero `Expiry` in a `jwt.Config` signs tokens that expire the moment
// they are issued, and a bare `&jwt.Config{}` has no signing method, so `Init`
// fails with `jwt: signing key not configured`."
func TestJWTWithConfig(t *testing.T) {
	t.Parallel()
	zero := hsConfig()
	zero.JWT.Expiry = 0
	svc := mustJWT(t, zero)
	tok, err := svc.Sign("u1", nil)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := svc.Authenticate(t.Context(), bearer(tok)); !errors.Is(err, auth.ErrCredentialsExpired) {
		t.Errorf("token signed with zero Expiry: %v, want expired", err)
	}
	bare := &jwt.Config{}
	bare.JWT.SigningKey = signingKey
	if _, err := newJWT(t, bare); err == nil || err.Error() != "jwt: signing key not configured" {
		t.Errorf("bare &jwt.Config{}: Init = %v, want jwt: signing key not configured", err)
	}

	// Bound, the same zero Expiry fails validation.
	b, err := jwt.New()(boundConfig(t, map[string]any{"jwt": map[string]any{"signingkey": signingKey, "expiry": "0s"}}), nopLogger())
	if err != nil {
		t.Fatalf("jwt.New: %v", err)
	}
	if err := b.Init(t.Context()); err == nil || !strings.Contains(err.Error(), "Expiry") {
		t.Errorf("bound zero expiry: Init = %v, want a validation error naming Expiry", err)
	}
	// The bound config is used: a bound issuer is set on signed tokens.
	b2, _ := jwt.New()(boundConfig(t, map[string]any{"jwt": map[string]any{"signingkey": signingKey, "issuer": "bound-iss"}}), nopLogger())
	if err := b2.Init(t.Context()); err != nil {
		t.Fatalf("bound Init: %v", err)
	}
	tok2, _ := b2.Sign("u", nil)
	if c, err := b2.Verify(tok2); err != nil || c.Issuer != "bound-iss" {
		t.Errorf("bound issuer not applied: %+v, %v", c, err)
	}
	// WithConfig replaces the bound config entirely.
	w, _ := jwt.New(jwt.WithConfig(hsConfig()))(boundConfig(t, map[string]any{"jwt": map[string]any{"signingkey": "short", "issuer": "bound-iss"}}), nopLogger())
	if err := w.Init(t.Context()); err != nil {
		t.Fatalf("WithConfig Init with a bad bound config: %v", err)
	}
	tok3, _ := w.Sign("u", nil)
	if c, _ := w.Verify(tok3); c.Issuer != "" {
		t.Errorf("WithConfig picked up the bound issuer %q", c.Issuer)
	}
}

// Docs: modules/auth.mdx#configuration
// "The HS256 key, RS256 public key path and cleanup-timeout rules are checked
// for every config, including one given with `WithConfig`."
// The cleanup-timeout cases are in session_test.go and token_test.go.
func TestJWTRulesForGivenConfig(t *testing.T) {
	t.Parallel()
	short := jwt.DefaultConfig()
	short.JWT.SigningKey = "short"
	empty := jwt.DefaultConfig()
	rs := jwt.DefaultConfig()
	rs.JWT.SigningMethod = "RS256"
	for name, cfg := range map[string]*jwt.Config{"short key": short, "no key": empty, "RS256 no public key": rs} {
		if _, err := newJWT(t, cfg); err == nil || !strings.HasPrefix(err.Error(), "gas/auth/jwt: ") {
			t.Errorf("%s via WithConfig: Init = %v, want a gas/auth/jwt error", name, err)
		}
	}
}

// Docs: modules/auth.mdx#configuration
// "| `jwt.signingkey` | string | empty | HMAC key for HS256. Required for HS256, at least 32 bytes. |"
// and "| `jwt.publickeypath` | string | empty | PEM RSA public key file. Required for RS256. |"
// and "| `jwt.privatekeypath` | string | empty |" and
// "| `jwt.issuer` | string | empty | Set as `iss` when signing and required when verifying, if not empty. |"
// and "| `jwt.audience` | string | empty | Set as `aud` when signing and required when verifying, if not empty. |"
// and "| `jwt.expiry` | duration | `15m` | Lifetime used by `Sign`. Must be greater than zero. |"
func TestJWTConfigTable(t *testing.T) {
	t.Parallel()
	d := jwt.DefaultConfig().JWT
	if d.SigningKey != "" || d.PublicKeyPath != "" || d.PrivateKeyPath != "" || d.Issuer != "" || d.Audience != "" || d.Expiry != 15*time.Minute {
		t.Errorf("defaults = %+v", d)
	}
	pub, priv := writeRSA(t, false)
	start := func(doc map[string]any) (*jwt.Service, error) {
		svc, err := jwt.New()(boundConfig(t, map[string]any{"jwt": doc}), nopLogger())
		if err != nil {
			t.Fatalf("jwt.New: %v", err)
		}
		return svc, svc.Init(t.Context())
	}
	svc, err := start(map[string]any{
		"signingmethod": "RS256", "publickeypath": pub, "privatekeypath": priv,
		"issuer": "i", "audience": "a", "expiry": "2m",
	})
	if err != nil {
		t.Fatalf("bound RS256 config: %v", err)
	}
	tok, _ := svc.Sign("u", nil)
	c, err := svc.Verify(tok)
	if err != nil || c.Issuer != "i" || c.Audience[0] != "a" || c.ExpiresAt.Sub(c.IssuedAt) != 2*time.Minute {
		t.Errorf("bound keys not applied: %+v, %v", c, err)
	}
	if _, err := start(map[string]any{"signingmethod": "ES256", "signingkey": signingKey}); err == nil {
		t.Error("bound signingmethod ES256: Init succeeded, want a validation error")
	}
	if _, err := start(map[string]any{"signingkey": signingKey, "expiry": "-1m"}); err == nil {
		t.Error("bound negative expiry: Init succeeded")
	}
	// Required when verifying, if not empty: a token with no iss fails a
	// service that has one; with none configured any iss passes.
	noIss := mustJWT(t, hsConfig())
	bareTok, _ := noIss.Sign("u", nil)
	issCfg := hsConfig()
	issCfg.JWT.Issuer = "need"
	if _, err := mustJWT(t, issCfg).Verify(bareTok); err == nil {
		t.Error("issuer configured: a token without iss verified")
	}
	audCfg := hsConfig()
	audCfg.JWT.Audience = "need"
	if _, err := mustJWT(t, audCfg).Verify(bareTok); err == nil {
		t.Error("audience configured: a token without aud verified")
	}
}

// Docs: modules/auth.mdx#errors
// "Other errors start with the service name (`gas/auth/jwt`,
// `gas/auth/session`, `gas/auth/apikey`, `gas/auth/token`) and wrap the
// underlying database, file or parsing error. The exceptions are the JWT
// service's `Sign` and `Verify` errors and `jwt: signing key not configured`,
// which start with `jwt:`."
func TestJWTErrorPrefixes(t *testing.T) {
	t.Parallel()
	missing := jwt.DefaultConfig()
	missing.JWT.SigningMethod = "RS256"
	missing.JWT.PublicKeyPath = filepath.Join(t.TempDir(), "absent.pem")
	_, err := newJWT(t, missing)
	if err == nil || !strings.HasPrefix(err.Error(), "gas/auth/jwt: ") || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing key file: %v, want gas/auth/jwt: wrapping fs.ErrNotExist", err)
	}
	notPEM := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(notPEM, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("junk")}), 0o600)
	missing.JWT.PublicKeyPath = notPEM
	if _, err := newJWT(t, missing); err == nil || !strings.HasPrefix(err.Error(), "gas/auth/jwt: ") || errors.Unwrap(err) == nil {
		t.Errorf("unparsable key: %v, want gas/auth/jwt: wrapping the parse error", err)
	}
	j := mustJWT(t, hsConfig())
	if _, err := j.Verify("garbage"); err == nil || !strings.HasPrefix(err.Error(), "jwt: ") {
		t.Errorf("Verify error = %v, want a jwt: prefix", err)
	}
	if _, err := j.Sign("u", map[string]any{"bad": make(chan int)}); err == nil || !strings.HasPrefix(err.Error(), "jwt: ") {
		t.Errorf("Sign error = %v, want a jwt: prefix", err)
	}
}
