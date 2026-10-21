package guidesauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
)

const signingKey = "a-signing-key-of-at-least-32-bytes!!"

var (
	authenticatorType = reflect.TypeFor[auth.Authenticator]()
	authorizerType    = reflect.TypeFor[auth.Authorizer]()
	revokerType       = reflect.TypeFor[auth.PrincipalRevoker]()
	configType        = reflect.TypeFor[gas.ConfigProvider]()
	loggerType        = reflect.TypeFor[gas.Logger]()
	dbType            = reflect.TypeFor[gas.DatabaseProvider]()
	migType           = reflect.TypeFor[gas.MigrationManager]()
)

func newJWT(t *testing.T, cfg *jwt.Config) (*jwt.Service, error) {
	t.Helper()
	svc, err := jwt.New(jwt.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		return nil, err
	}
	if err := svc.Init(t.Context()); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, nil
}

func mustJWT(t *testing.T) *jwt.Service {
	t.Helper()
	cfg := jwt.DefaultConfig()
	cfg.JWT.SigningKey = signingKey
	svc, err := newJWT(t, cfg)
	if err != nil {
		t.Fatalf("jwt service: %v", err)
	}
	return svc
}

func bearer(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

// sessionRequest creates a session for subject and returns a request that
// carries the cookie SetCookie wrote.
func sessionRequest(t *testing.T, svc *session.Service, subject string, meta gas.BasePrincipalMetadata) (*http.Request, *session.Session) {
	t.Helper()
	s, err := svc.Create(t.Context(), subject, meta, httptest.NewRequest(http.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := httptest.NewRecorder()
	svc.SetCookie(rec, s)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r, s
}

func apiKeyRequest(key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(apikey.DefaultConfig().APIKey.HeaderName, key)
	return r
}

func generate(t *testing.T, svc apikey.Provider, subject string, scopes ...string) (string, *apikey.KeyInfo) {
	t.Helper()
	key, info, err := svc.Generate(t.Context(), subject, "ci", scopes)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return key, info
}

func principal(subject, scheme string) gas.Principal {
	return auth.NewPrincipal(subject, scheme, "cred-"+subject, nil)
}

func fixed(p gas.Principal) *authtest.MockAuthenticator {
	return &authtest.MockAuthenticator{AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
		return p, nil
	}}
}

// serve runs mw around a handler that records the principal on the context.
func serve(mw func(http.Handler) http.Handler, r *http.Request) (*httptest.ResponseRecorder, gas.Principal, bool) {
	var got gas.Principal
	reached := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		got, _ = gas.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, got, reached
}

// Docs: guides/auth.mdx#_top
// "`gas/auth` provides four credential types. `jwt`, `session`, and `apikey` sit
// behind one interface, `auth.Authenticator`; `token` issues single-use tokens
// that are verified once and consumed, so it is not an authenticator."
func TestThreeCredentialTypesAreAuthenticators(t *testing.T) {
	t.Parallel()
	for name, typ := range map[string]reflect.Type{
		"jwt":     reflect.TypeFor[*jwt.Service](),
		"session": reflect.TypeFor[*session.Service](),
		"apikey":  reflect.TypeFor[*apikey.Service](),
	} {
		if !typ.Implements(authenticatorType) {
			t.Errorf("%s: %v does not implement auth.Authenticator", name, typ)
		}
	}
	if typ := reflect.TypeFor[*token.Service](); typ.Implements(authenticatorType) {
		t.Errorf("token: %v implements auth.Authenticator; want it not to", typ)
	}
}

// Docs: guides/auth.mdx#_top
// "The module declares `auth.Authenticator`, `auth.Authorizer`, and
// `auth.PrincipalRevoker`, implements the first and the last, and adds
// middleware. `auth.Authorizer` is for the application to implement; the
// module ships only a mock of it, in `authtest`."
func TestModuleImplementsAuthenticatorAndRevoker(t *testing.T) {
	t.Parallel()
	// The module's non-mock exported types; the authtest mocks are counted
	// separately.
	types := []reflect.Type{
		reflect.TypeFor[*jwt.Service](),
		reflect.TypeFor[*session.Service](),
		reflect.TypeFor[*apikey.Service](),
		reflect.TypeFor[*token.Service](),
		reflect.TypeFor[auth.Chain](),
		reflect.TypeFor[*auth.BasePrincipal](),
	}
	for _, iface := range []reflect.Type{authenticatorType, revokerType} {
		if !slices.ContainsFunc(types, func(t reflect.Type) bool { return t.Implements(iface) }) {
			t.Errorf("no type in the module implements %v", iface)
		}
	}
	for _, typ := range types {
		if typ.Implements(authorizerType) {
			t.Errorf("%v implements auth.Authorizer; want only the authtest mock to", typ)
		}
	}
	if typ := reflect.TypeFor[*authtest.MockAuthorizer](); !typ.Implements(authorizerType) {
		t.Errorf("%v does not implement auth.Authorizer", typ)
	}
	if auth.Middleware(&authtest.MockAuthenticator{}) == nil {
		t.Error("auth.Middleware returned nil")
	}
}

// Docs: guides/auth.mdx#_top
// "Core owns only the `gas.Principal` the middleware stores on the request,
// which a handler reads with `ctx.Principal()`."
func TestHandlerReadsPrincipalWithContextPrincipal(t *testing.T) {
	t.Parallel()
	want := principal("u1", auth.SchemeJWT)
	var got gas.Principal
	var gotErr error
	h := auth.Middleware(fixed(want))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotErr = gas.NewContext(r.Context(), w, r).Principal()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if gotErr != nil || got != want {
		t.Fatalf("ctx.Principal() = %v, %v; want %v, nil", got, gotErr, want)
	}
}

// Docs: guides/auth.mdx#_top
// "Stateless, HS256 or RS256. No database."
func TestJWTStatelessHS256OrRS256NoDatabase(t *testing.T) {
	t.Parallel()
	ctor := reflect.TypeOf(jwt.New())
	for i := range ctor.NumIn() {
		if in := ctor.In(i); in == dbType || in == migType {
			t.Errorf("jwt constructor takes %v", in)
		}
	}

	t.Run("HS256", func(t *testing.T) {
		t.Parallel()
		cfg := jwt.DefaultConfig()
		cfg.JWT.SigningKey = signingKey
		cfg.JWT.SigningMethod = "HS256"
		svc, err := newJWT(t, cfg)
		if err != nil {
			t.Fatalf("HS256 service: %v", err)
		}
		tok, err := svc.Sign("u1", nil)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if c, err := svc.Verify(tok); err != nil || c.Subject != "u1" {
			t.Fatalf("Verify = %+v, %v; want subject u1", c, err)
		}
	})

	t.Run("RS256", func(t *testing.T) {
		t.Parallel()
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		privPath, pubPath := filepath.Join(dir, "key.pem"), filepath.Join(dir, "key.pub.pem")
		// Assumption: keys in the common PEM encodings (PKCS#1 private, PKIX
		// public); the page names no format.
		if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := jwt.DefaultConfig()
		cfg.JWT.SigningMethod = "RS256"
		cfg.JWT.PrivateKeyPath = privPath
		cfg.JWT.PublicKeyPath = pubPath
		svc, err := newJWT(t, cfg)
		if err != nil {
			t.Fatalf("RS256 service: %v", err)
		}
		tok, err := svc.Sign("u1", nil)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if c, err := svc.Verify(tok); err != nil || c.Subject != "u1" {
			t.Fatalf("Verify = %+v, %v; want subject u1", c, err)
		}
	})
}

// Docs: guides/auth.mdx#_top
// "Server-side sessions with a cookie. Revocable."
func TestSessionServerSideCookieRevocable(t *testing.T) {
	t.Parallel()
	svc := newSessionOn(t, sqliteBackend(t).open(t))
	r, s := sessionRequest(t, svc, "u1", nil)
	if len(r.Cookies()) == 0 {
		t.Fatal("SetCookie wrote no cookie")
	}
	p, err := svc.Authenticate(t.Context(), r)
	if err != nil || p.Subject() != "u1" {
		t.Fatalf("Authenticate = %v, %v; want subject u1", p, err)
	}
	if err := svc.Revoke(t.Context(), p); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if p, err := svc.Authenticate(t.Context(), r); err == nil {
		t.Fatalf("Authenticate after Revoke of session %s = %v, nil; want an error", s.ID, p)
	}
}

// Docs: guides/auth.mdx#_top
// "Hashed keys with scopes, for machine callers."
func TestAPIKeyHashedWithScopes(t *testing.T) {
	t.Parallel()
	svc := newAPIKeyOn(t, sqliteBackend(t).open(t))
	key, info := generate(t, svc, "svc-1", "read", "write")
	if info.KeyHash == "" || info.KeyHash == key {
		t.Fatalf("KeyHash = %q; want a hash distinct from the key", info.KeyHash)
	}
	p, err := svc.Authenticate(t.Context(), apiKeyRequest(key))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	got, ok := gas.MetadataValue[[]string](p.Metadata(), apikey.PrincipalMetadataKeyScopes)
	if !ok || !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("scopes metadata = %v, %v; want [read write], true (raw %#v)", got, ok, p.Metadata().Value(apikey.PrincipalMetadataKeyScopes))
	}
}

// Docs: guides/auth.mdx#_top
// "Single-use, expiring."
func TestTokenSingleUseExpiring(t *testing.T) {
	t.Parallel()
	svc := newTokenOn(t, sqliteBackend(t).open(t))
	raw, err := svc.Issue(t.Context(), "u1", "reset", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if sub, err := svc.Verify(t.Context(), raw, "reset"); err != nil || sub != "u1" {
		t.Fatalf("first Verify = %q, %v; want u1, nil", sub, err)
	}
	if _, err := svc.Verify(t.Context(), raw, "reset"); err == nil {
		t.Fatal("second Verify succeeded; want an error")
	}

	short, err := svc.Issue(t.Context(), "u1", "reset", time.Second)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := svc.Verify(t.Context(), short, "reset"); !errors.Is(err, token.ErrTokenExpired) {
		t.Fatalf("Verify after expiry = %v; want token.ErrTokenExpired", err)
	}
}

// Docs: guides/auth.mdx#wiring
// "`jwt` is stateless: it needs a `gas.ConfigProvider` and a `gas.Logger`, and no
// database."
func TestJWTNeedsConfigAndLoggerOnly(t *testing.T) {
	t.Parallel()
	ctor := reflect.TypeOf(jwt.New())
	var in []reflect.Type
	for i := range ctor.NumIn() {
		in = append(in, ctor.In(i))
	}
	if want := []reflect.Type{configType, loggerType}; !slices.Equal(in, want) {
		t.Fatalf("jwt.New() constructor parameters = %v; want %v", in, want)
	}
}

// Docs: guides/auth.mdx#wiring
// "`session`, `apikey`, and `token` persist credentials and register their own
// migrations, so they need `gas.DatabaseProvider` and `gas.MigrationManager`."
func TestStatefulServicesPersistAndRegisterMigrations(t *testing.T) {
	t.Parallel()
	for name, ctor := range map[string]any{"session": session.New(), "apikey": apikey.New(), "token": token.New()} {
		typ := reflect.TypeOf(ctor)
		var in []reflect.Type
		for i := range typ.NumIn() {
			in = append(in, typ.In(i))
		}
		if !slices.Contains(in, dbType) || !slices.Contains(in, migType) {
			t.Errorf("%s constructor parameters = %v; want gas.DatabaseProvider and gas.MigrationManager", name, in)
		}
	}

	countApplied := func(t *testing.T, db *database.Service) int {
		t.Helper()
		var n int
		if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM __gas_migrations").Scan(&n); err != nil {
			t.Fatalf("count __gas_migrations: %v", err)
		}
		return n
	}

	// A second service instance on the same database observes what the first
	// one stored, so the credential lives in the database, not in memory.
	t.Run("session", func(t *testing.T) {
		t.Parallel()
		b := sqliteBackend(t)
		db := b.open(t)
		r, _ := sessionRequest(t, newSessionOn(t, db), "u1", nil)
		if countApplied(t, db) == 0 {
			t.Fatal("no migrations recorded after session Init and RunPending")
		}
		if _, err := newSessionOn(t, b.open(t)).Authenticate(t.Context(), r); err != nil {
			t.Fatalf("second instance Authenticate: %v", err)
		}
	})
	t.Run("apikey", func(t *testing.T) {
		t.Parallel()
		b := sqliteBackend(t)
		db := b.open(t)
		key, _ := generate(t, newAPIKeyOn(t, db), "svc-1")
		if countApplied(t, db) == 0 {
			t.Fatal("no migrations recorded after apikey Init and RunPending")
		}
		if _, err := newAPIKeyOn(t, b.open(t)).Authenticate(t.Context(), apiKeyRequest(key)); err != nil {
			t.Fatalf("second instance Authenticate: %v", err)
		}
	})
	t.Run("token", func(t *testing.T) {
		t.Parallel()
		b := sqliteBackend(t)
		db := b.open(t)
		raw, err := newTokenOn(t, db).Issue(t.Context(), "u1", "verify", 0)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if countApplied(t, db) == 0 {
			t.Fatal("no migrations recorded after token Init and RunPending")
		}
		if sub, err := newTokenOn(t, b.open(t)).Verify(t.Context(), raw, "verify"); err != nil || sub != "u1" {
			t.Fatalf("second instance Verify = %q, %v; want u1, nil", sub, err)
		}
	})
}

// Docs: guides/auth.mdx#wiring
// "Build configs from `DefaultConfig()`. A bare `&jwt.Config{}` literal leaves
// `SigningMethod` empty, so no signing key is set up and startup fails with
// `jwt: signing key not configured`."
func TestJWTBareConfigFailsWithoutSigningKey(t *testing.T) {
	t.Parallel()
	ok := jwt.DefaultConfig()
	ok.JWT.SigningKey = signingKey
	if _, err := newJWT(t, ok); err != nil {
		t.Fatalf("DefaultConfig with a signing key: %v; want startup to succeed", err)
	}

	bare := &jwt.Config{}
	if bare.JWT.SigningMethod != "" {
		t.Fatalf("bare SigningMethod = %q; want empty", bare.JWT.SigningMethod)
	}
	// A signing key is supplied so the failure can only come from the empty
	// SigningMethod.
	bare.JWT.SigningKey = signingKey
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](&configtest.MockConfig{}),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithSingletonService[*jwt.Service](jwt.New(jwt.WithConfig(bare))),
	)
	err := w.Start(t.Context())
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err == nil || !strings.Contains(err.Error(), "jwt: signing key not configured") {
		t.Fatalf("Worker.Start with a bare &jwt.Config{} = %v; want an error containing %q", err, "jwt: signing key not configured")
	}
}

// Docs: guides/auth.mdx#wiring
// "A config passed with `jwt.WithConfig` is used as given, without its validate
// tags, so a zero `Expiry` there signs tokens that expire the moment they are
// issued."
func TestJWTWithConfigZeroExpiryTokensExpireAtOnce(t *testing.T) {
	t.Parallel()
	zero := jwt.DefaultConfig()
	zero.JWT.SigningKey = signingKey
	zero.JWT.Expiry = 0
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](&configtest.MockConfig{}),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithSingletonService[*jwt.Service](jwt.New(jwt.WithConfig(zero))),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Worker.Start with WithConfig and Expiry 0 = %v; want nil (validate tags not applied)", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	svc, err := w.ServiceContainer().Resolve[*jwt.Service]()
	if err != nil {
		t.Fatalf("resolve *jwt.Service: %v", err)
	}
	tok, err := svc.Sign("u1", nil)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if p, err := svc.Authenticate(t.Context(), bearer(tok)); err == nil {
		t.Fatalf("Authenticate of a freshly issued token = %v, nil; want an error (already expired)", p)
	}
}

// Docs: guides/auth.mdx#wiring
// "A zero `Expiry` fails validation at startup only when the config is bound
// from the `gas.ConfigProvider`."
func TestJWTBoundZeroExpiryFailsValidation(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, expiry string) error {
		t.Helper()
		body := `{"jwt":{"signingKey":"` + signingKey + `","signingMethod":"HS256","expiry":"` + expiry + `"}}`
		fsys := fstest.MapFS{"config.json": {Data: []byte(body)}}
		cfg := config.New(config.WithProvider(providers.NewJSONProvider(
			providers.WithJSONFileFS(fsys), providers.WithJSONFilePath("config.json"))))
		if err := cfg.Load(); err != nil {
			t.Fatalf("config Load: %v", err)
		}
		w := gas.NewWorker(
			gas.WithServiceInstance[gas.ConfigProvider](cfg),
			gas.WithServiceInstance[gas.Logger](nopLogger()),
			gas.WithSingletonService[*jwt.Service](jwt.New()),
		)
		err := w.Start(t.Context())
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		return err
	}
	if err := start(t, "1h"); err != nil {
		t.Fatalf("Start with a bound Expiry of 1h = %v; want nil", err)
	}
	err := start(t, "0s")
	if err == nil {
		t.Fatal("Start with a bound Expiry of 0s succeeded; want a validation error")
	}
	if !strings.Contains(err.Error(), "Expiry") {
		t.Fatalf("Start with a bound Expiry of 0s = %v; want a validation error naming Expiry", err)
	}
}

// Docs: guides/auth.mdx#protecting-routes
// "`auth.Middleware` runs an authenticator and puts the resulting
// `gas.Principal` on the request context."
func TestMiddlewarePutsPrincipalOnContext(t *testing.T) {
	t.Parallel()
	svc := mustJWT(t)
	tok, err := svc.Sign("u1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, got, reached := serve(auth.Middleware(svc), bearer(tok))
	if !reached || got == nil || got.Subject() != "u1" || got.Scheme() != auth.SchemeJWT {
		t.Fatalf("principal on context = %v (reached %v); want subject u1 scheme jwt", got, reached)
	}
}

// Docs: guides/auth.mdx#protecting-routes
// "`auth.Chain` tries several in order, so one group can accept a JWT, a
// session cookie, or an API key."
func TestChainAcceptsJWTSessionOrAPIKey(t *testing.T) {
	t.Parallel()
	db := sqliteBackend(t).open(t)
	jwtSvc := mustJWT(t)
	sessSvc := newSessionOn(t, db)
	keySvc := newAPIKeyOn(t, db)
	mw := auth.Middleware(auth.Chain{jwtSvc, sessSvc, keySvc})

	tok, err := jwtSvc.Sign("jwt-user", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessReq, _ := sessionRequest(t, sessSvc, "session-user", nil)
	key, _ := generate(t, keySvc, "key-user")

	for _, tc := range []struct {
		r       *http.Request
		subject string
		scheme  string
	}{
		{bearer(tok), "jwt-user", auth.SchemeJWT},
		{sessReq, "session-user", auth.SchemeSession},
		{apiKeyRequest(key), "key-user", auth.SchemeAPIKey},
	} {
		_, p, reached := serve(mw, tc.r)
		if !reached || p == nil || p.Subject() != tc.subject || p.Scheme() != tc.scheme {
			t.Errorf("%s: principal = %v (reached %v); want %s/%s", tc.scheme, p, reached, tc.subject, tc.scheme)
		}
	}

	// In order: the first member that succeeds wins.
	first, second := fixed(principal("a", "x")), fixed(principal("b", "y"))
	p, err := auth.Chain{first, second}.Authenticate(t.Context(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || p.Subject() != "a" || second.CallCount("Authenticate") != 0 {
		t.Fatalf("Chain = %v, %v, second called %d times; want the first member's principal, second untried", p, err, second.CallCount("Authenticate"))
	}
}

// Docs: guides/auth.mdx#protecting-routes
// "Add `auth.RequireScheme(auth.SchemeSession)` when a route must insist on one
// specific credential type, for example a settings page that a long-lived API
// key should not reach."
func TestRequireSchemeInsistsOnOneType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scheme string
		reach  bool
	}{
		{auth.SchemeSession, true},
		{auth.SchemeAPIKey, false},
		{auth.SchemeJWT, false},
	} {
		mw := func(next http.Handler) http.Handler {
			return auth.Middleware(fixed(principal("u1", tc.scheme)))(auth.RequireScheme(auth.SchemeSession)(next))
		}
		rec, _, reached := serve(mw, httptest.NewRequest(http.MethodGet, "/settings", nil))
		if reached != tc.reach {
			t.Errorf("scheme %s: handler reached = %v (status %d); want %v", tc.scheme, reached, rec.Code, tc.reach)
		}
	}
}

// Docs: guides/auth.mdx#reading-the-principal
// "A `Principal` carries `Subject()` (the stable user id), `Scheme()` (`jwt`,
// `session`, `apikey`), `CredentialID()` (the specific session or key), and
// metadata."
func TestPrincipalCarriesSubjectSchemeCredentialMetadata(t *testing.T) {
	t.Parallel()
	db := sqliteBackend(t).open(t)

	t.Run("jwt", func(t *testing.T) {
		svc := mustJWT(t)
		tok, err := svc.Sign("u1", map[string]any{"role": "admin"})
		if err != nil {
			t.Fatal(err)
		}
		p, err := svc.Authenticate(t.Context(), bearer(tok))
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if p.Subject() != "u1" || p.Scheme() != "jwt" {
			t.Errorf("Subject, Scheme = %q, %q; want u1, jwt", p.Subject(), p.Scheme())
		}
		if role, _ := gas.MetadataValue[string](p.Metadata(), "role"); role != "admin" {
			t.Errorf("metadata role = %q; want admin", role)
		}
	})
	t.Run("session", func(t *testing.T) {
		svc := newSessionOn(t, db)
		r, s := sessionRequest(t, svc, "u1", gas.BasePrincipalMetadata{"role": "admin"})
		p, err := svc.Authenticate(t.Context(), r)
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if p.Subject() != "u1" || p.Scheme() != "session" || p.CredentialID() != s.ID {
			t.Errorf("Subject, Scheme, CredentialID = %q, %q, %q; want u1, session, %q", p.Subject(), p.Scheme(), p.CredentialID(), s.ID)
		}
		if role, _ := gas.MetadataValue[string](p.Metadata(), "role"); role != "admin" {
			t.Errorf("metadata role = %q; want admin", role)
		}
	})
	t.Run("apikey", func(t *testing.T) {
		svc := newAPIKeyOn(t, db)
		key, info := generate(t, svc, "u1", "read")
		p, err := svc.Authenticate(t.Context(), apiKeyRequest(key))
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if p.Subject() != "u1" || p.Scheme() != "apikey" || p.CredentialID() != info.ID {
			t.Errorf("Subject, Scheme, CredentialID = %q, %q, %q; want u1, apikey, %q", p.Subject(), p.Scheme(), p.CredentialID(), info.ID)
		}
		if p.Metadata() == nil {
			t.Error("Metadata() = nil")
		}
	})
}

// Docs: guides/auth.mdx#reading-the-principal
// "`gas.MetadataValue[T]` reads metadata without an unchecked type assertion."
func TestMetadataValueChecksType(t *testing.T) {
	t.Parallel()
	md := gas.BasePrincipalMetadata{"role": "admin"}
	if v, ok := gas.MetadataValue[string](md, "role"); !ok || v != "admin" {
		t.Fatalf("MetadataValue[string] = %q, %v; want admin, true", v, ok)
	}
	if v, ok := gas.MetadataValue[int](md, "role"); ok || v != 0 {
		t.Fatalf("MetadataValue[int] of a string = %v, %v; want 0, false", v, ok)
	}
	if v, ok := gas.MetadataValue[string](md, "missing"); ok || v != "" {
		t.Fatalf("MetadataValue of a missing key = %q, %v; want \"\", false", v, ok)
	}
}

// Docs: guides/auth.mdx#issuing-credentials
// "Sessions are created with `Create(ctx, subject, meta, r)` and written with
// `SetCookie`."
func TestSessionCreateAndSetCookie(t *testing.T) {
	t.Parallel()
	svc := newSessionOn(t, sqliteBackend(t).open(t))
	s, err := svc.Create(t.Context(), "u1", nil, httptest.NewRequest(http.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := httptest.NewRecorder()
	svc.SetCookie(rec, s)
	name := session.DefaultConfig().Session.CookieName
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value == s.ID {
			return
		}
	}
	t.Fatalf("Set-Cookie = %v; want cookie %q with the session ID", rec.Result().Header["Set-Cookie"], name)
}

// Docs: guides/auth.mdx#issuing-credentials
// "API keys come from `Generate(ctx, subject, name, scopes, opts...)`, which
// returns the plaintext key **exactly once**; only a SHA-256 hash is stored."
func TestAPIKeyPlaintextOnceSHA256Stored(t *testing.T) {
	t.Parallel()
	db := sqliteBackend(t).open(t)
	svc := newAPIKeyOn(t, db)
	key, info := generate(t, svc, "u1", "read")

	sum := sha256.Sum256([]byte(key))
	hashes := []string{
		hex.EncodeToString(sum[:]),
		base64.StdEncoding.EncodeToString(sum[:]),
		base64.RawURLEncoding.EncodeToString(sum[:]),
	}
	if !slices.Contains(hashes, info.KeyHash) {
		t.Errorf("KeyHash = %q; want an encoding of SHA-256(key)", info.KeyHash)
	}

	list, err := svc.List(t.Context(), "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range list {
		if strings.Contains(k.KeyHash, key) || strings.Contains(k.KeyPrefix, key) || strings.Contains(k.Name, key) {
			t.Errorf("List exposes the plaintext key: %+v", k)
		}
	}

	// No stored value anywhere in the database is the plaintext key.
	tables, err := db.DB().QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = tables.Close()
	for _, n := range names {
		rows, err := db.DB().QueryContext(t.Context(), `SELECT * FROM "`+n+`"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				var s string
				switch v := v.(type) {
				case string:
					s = v
				case []byte:
					s = string(v)
				}
				if s != "" && strings.Contains(s, key) {
					t.Errorf("table %s column %s stores the plaintext key", n, cols[i])
				}
			}
		}
		_ = rows.Close()
	}
}

// Docs: guides/auth.mdx#issuing-credentials
// "Single-use tokens come from `Issue` and are consumed by `Verify`, which
// fails on the second attempt."
func TestTokenVerifyFailsSecondAttempt(t *testing.T) {
	t.Parallel()
	svc := newTokenOn(t, sqliteBackend(t).open(t))
	raw, err := svc.Issue(t.Context(), "u1", "magic", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Verify(t.Context(), raw, "magic"); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if sub, err := svc.Verify(t.Context(), raw, "magic"); err == nil {
		t.Fatalf("second Verify = %q, nil; want an error", sub)
	}
}

// Docs: guides/auth.mdx#error-responses
// "By default a failed authentication writes a plain `401`."
func TestDefaultFailureIsPlain401(t *testing.T) {
	t.Parallel()
	rec, _, reached := serve(auth.Middleware(&authtest.MockAuthenticator{}), httptest.NewRequest(http.MethodGet, "/", nil))
	if reached {
		t.Fatal("handler reached on failed authentication")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", rec.Code)
	}
	// Assumption: "plain" means a text/plain body, not JSON.
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q; want text/plain", ct)
	}
}

// Docs: guides/auth.mdx#error-responses
// "To return the unified error shape instead, take over the response:"
func TestWithOnErrorTakesOverResponse(t *testing.T) {
	t.Parallel()
	var gotErr error
	mw := auth.Middleware(&authtest.MockAuthenticator{}, auth.WithOnError(func(w http.ResponseWriter, _ *http.Request, err error) {
		gotErr = err
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	}))
	rec, _, reached := serve(mw, httptest.NewRequest(http.MethodGet, "/", nil))
	if reached {
		t.Fatal("handler reached on failed authentication")
	}
	if rec.Code != http.StatusTeapot || rec.Body.String() != `{"error":"x"}` {
		t.Fatalf("response = %d %q; want the OnError handler's 418 body", rec.Code, rec.Body.String())
	}
	if !errors.Is(gotErr, auth.ErrUnauthenticated) {
		t.Fatalf("OnError err = %v; want the authenticator's auth.ErrUnauthenticated", gotErr)
	}
}

// revocable is one credential type behind auth.PrincipalRevoker, with a way
// to issue a credential and check whether it still authenticates.
type revocable struct {
	scheme  string
	revoker auth.PrincipalRevoker
	issue   func(t *testing.T, subject string) (gas.Principal, func() error)
}

func revocables(t *testing.T) []revocable {
	t.Helper()
	db := sqliteBackend(t).open(t)
	sess := newSessionOn(t, db)
	keys := newAPIKeyOn(t, db)
	return []revocable{
		{auth.SchemeSession, sess, func(t *testing.T, subject string) (gas.Principal, func() error) {
			r, _ := sessionRequest(t, sess, subject, nil)
			p, err := sess.Authenticate(t.Context(), r)
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			return p, func() error { _, err := sess.Authenticate(t.Context(), r); return err }
		}},
		{auth.SchemeAPIKey, keys, func(t *testing.T, subject string) (gas.Principal, func() error) {
			key, _ := generate(t, keys, subject)
			r := apiKeyRequest(key)
			p, err := keys.Authenticate(t.Context(), r)
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			return p, func() error { _, err := keys.Authenticate(t.Context(), apiKeyRequest(key)); return err }
		}},
	}
}

// Docs: guides/auth.mdx#revoking
// "`session` and `apikey` implement `auth.PrincipalRevoker`: `Revoke` kills one
// credential, `RevokeAll` every credential for a subject, and
// `RevokeAllByScheme` every credential of one type."
func TestSessionAndAPIKeyRevoke(t *testing.T) {
	t.Parallel()
	var _ auth.PrincipalRevoker = (*session.Service)(nil)
	var _ auth.PrincipalRevoker = (*apikey.Service)(nil)

	for _, rv := range revocables(t) {
		t.Run(rv.scheme+"/Revoke", func(t *testing.T) {
			p1, alive1 := rv.issue(t, "r1")
			_, alive2 := rv.issue(t, "r1")
			if err := rv.revoker.Revoke(t.Context(), p1); err != nil {
				t.Fatalf("Revoke: %v", err)
			}
			if alive1() == nil {
				t.Error("revoked credential still authenticates")
			}
			if err := alive2(); err != nil {
				t.Errorf("other credential of the subject: %v; want it to survive", err)
			}
		})
		t.Run(rv.scheme+"/RevokeAll", func(t *testing.T) {
			_, a := rv.issue(t, "r2")
			_, b := rv.issue(t, "r2")
			_, other := rv.issue(t, "r2-other")
			if err := rv.revoker.RevokeAll(t.Context(), "r2"); err != nil {
				t.Fatalf("RevokeAll: %v", err)
			}
			if a() == nil || b() == nil {
				t.Error("a credential of the subject survived RevokeAll")
			}
			if err := other(); err != nil {
				t.Errorf("another subject's credential: %v; want it to survive", err)
			}
		})
		t.Run(rv.scheme+"/RevokeAllByScheme", func(t *testing.T) {
			_, a := rv.issue(t, "r3")
			mismatch := auth.SchemeAPIKey
			if rv.scheme == auth.SchemeAPIKey {
				mismatch = auth.SchemeSession
			}
			if err := rv.revoker.RevokeAllByScheme(t.Context(), "r3", mismatch); err != nil {
				t.Fatalf("RevokeAllByScheme(other scheme): %v", err)
			}
			if err := a(); err != nil {
				t.Errorf("credential after RevokeAllByScheme of another type: %v; want it to survive", err)
			}
			_, b := rv.issue(t, "r3")
			if err := rv.revoker.RevokeAllByScheme(t.Context(), "r3", rv.scheme); err != nil {
				t.Fatalf("RevokeAllByScheme: %v", err)
			}
			if a() == nil || b() == nil {
				t.Error("a credential of the type survived RevokeAllByScheme")
			}
		})
	}
}

// Docs: guides/auth.mdx#revoking
// "JWTs are stateless and cannot be revoked individually"
func TestJWTCannotBeRevoked(t *testing.T) {
	t.Parallel()
	if reflect.TypeFor[*jwt.Service]().Implements(revokerType) {
		t.Fatal("*jwt.Service implements auth.PrincipalRevoker")
	}
	if _, ok := reflect.TypeFor[*jwt.Service]().MethodByName("Revoke"); ok {
		t.Fatal("*jwt.Service has a Revoke method")
	}
}

// Docs: guides/auth.mdx#revoking
// "API key operations can join your own transaction. `apiKeyService.WithTx(tx)`
// returns a provider scoped to that transaction, so creating a user and their
// first key either both happen or neither does."
func TestAPIKeyWithTxAtomicWithCallerWrites(t *testing.T) {
	t.Parallel()
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := sqliteBackend(t).open(t)
			svc := newAPIKeyOn(t, db)
			if _, err := db.DB().ExecContext(t.Context(), "CREATE TABLE users (id TEXT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO users (id) VALUES ('u1')"); err != nil {
				t.Fatal(err)
			}
			key, _ := generate(t, svc.WithTx(tx), "u1")
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatalf("end tx: %v", err)
			}

			var users int
			if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM users").Scan(&users); err != nil {
				t.Fatal(err)
			}
			_, authErr := svc.Authenticate(t.Context(), apiKeyRequest(key))
			userExists, keyExists := users == 1, authErr == nil
			if userExists != commit || keyExists != commit {
				t.Fatalf("after %s: user exists %v, key authenticates %v (%v); want both %v", name, userExists, keyExists, authErr, commit)
			}
		})
	}
}

// Docs: guides/auth.mdx#databases
// "`session`, `apikey`, and `token` support PostgreSQL and SQLite, and pick the
// dialect from the `gas.DatabaseProvider`'s `Driver()`."
//
// MySQL runs in TestStoresSupportMySQL, on the same checks.
func TestStoresSupportPostgresAndSQLite(t *testing.T) {
	t.Parallel()
	for i, mk := range allBackends(t) {
		t.Run(backendNames[i], func(t *testing.T) {
			t.Parallel()
			checkStores(t, func(t *testing.T) gas.DatabaseProvider { return mk(t).open(t) }, nil)
		})
	}
}

// checkStores runs one session, API key and single-use token round trip,
// each as its own subtest on a fresh database from open, so a store that
// fails (and leaves a dirty migration behind) cannot hide the others. skip
// maps a store name to the t.Skip message for a known issue.
func checkStores(t *testing.T, open func(t *testing.T) gas.DatabaseProvider, skip map[string]string) {
	t.Helper()
	t.Run("session", func(t *testing.T) {
		if msg, ok := skip["session"]; ok {
			t.Skip(msg)
		}
		sess := newSessionOn(t, open(t))
		r, _ := sessionRequest(t, sess, "u1", nil)
		if p, err := sess.Authenticate(t.Context(), r); err != nil || p.Subject() != "u1" {
			t.Errorf("session Authenticate = %v, %v", p, err)
		}
	})
	t.Run("apikey", func(t *testing.T) {
		if msg, ok := skip["apikey"]; ok {
			t.Skip(msg)
		}
		keys := newAPIKeyOn(t, open(t))
		key, _ := generate(t, keys, "u1", "read")
		if p, err := keys.Authenticate(t.Context(), apiKeyRequest(key)); err != nil || p.Subject() != "u1" {
			t.Errorf("apikey Authenticate = %v, %v", p, err)
		}
	})
	t.Run("token", func(t *testing.T) {
		if msg, ok := skip["token"]; ok {
			t.Skip(msg)
		}
		toks := newTokenOn(t, open(t))
		raw, err := toks.Issue(t.Context(), "u1", "verify", 0)
		if err != nil {
			t.Fatalf("token Issue: %v", err)
		}
		if sub, err := toks.Verify(t.Context(), raw, "verify"); err != nil || sub != "u1" {
			t.Errorf("token Verify = %q, %v", sub, err)
		}
	})
}

// Docs: guides/auth.mdx#testing
// "`authtest` provides `MockAuthenticator`, `MockAuthorizer`, and
// `MockRevoker`, each recording calls and delegating to an optional `Fn`
// field, so a protected handler can be tested without issuing real
// credentials."
func TestAuthtestMocksRecordAndDelegate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	p := principal("u1", auth.SchemeSession)
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	t.Run("MockAuthenticator", func(t *testing.T) {
		unset := &authtest.MockAuthenticator{}
		if _, err := unset.Authenticate(ctx, r); err == nil {
			t.Error("Authenticate with no Fn succeeded")
		}
		m := fixed(p)
		_, got, reached := serve(auth.Middleware(m), r)
		if !reached || got != p {
			t.Errorf("protected handler principal = %v (reached %v); want the mock's", got, reached)
		}
		if m.CallCount("Authenticate") != 1 || len(m.Calls) != 1 {
			t.Errorf("CallCount = %d, Calls = %v; want 1 recorded call", m.CallCount("Authenticate"), m.Calls)
		}
	})
	t.Run("MockAuthorizer", func(t *testing.T) {
		m := &authtest.MockAuthorizer{}
		if err := m.Authorize(ctx, p, "read", "doc"); err != nil {
			t.Errorf("Authorize with no Fn = %v", err)
		}
		m.AuthorizeFn = func(context.Context, gas.Principal, string, string) error { return auth.ErrForbidden }
		if err := m.Authorize(ctx, p, "write", "doc"); !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("Authorize with Fn = %v; want auth.ErrForbidden", err)
		}
		if m.CallCount("Authorize") != 2 || len(m.Calls) != 2 {
			t.Errorf("CallCount = %d, Calls = %v; want 2", m.CallCount("Authorize"), m.Calls)
		}
	})
	t.Run("MockRevoker", func(t *testing.T) {
		want := errors.New("boom")
		m := &authtest.MockRevoker{
			RevokeFn:            func(context.Context, gas.Principal) error { return want },
			RevokeAllFn:         func(context.Context, string) error { return want },
			RevokeAllBySchemeFn: func(context.Context, string, string) error { return want },
		}
		if err := m.Revoke(ctx, p); !errors.Is(err, want) {
			t.Errorf("Revoke = %v", err)
		}
		if err := m.RevokeAll(ctx, "u1"); !errors.Is(err, want) {
			t.Errorf("RevokeAll = %v", err)
		}
		if err := m.RevokeAllByScheme(ctx, "u1", auth.SchemeSession); !errors.Is(err, want) {
			t.Errorf("RevokeAllByScheme = %v", err)
		}
		for _, method := range []string{"Revoke", "RevokeAll", "RevokeAllByScheme"} {
			if m.CallCount(method) != 1 {
				t.Errorf("CallCount(%s) = %d; want 1", method, m.CallCount(method))
			}
		}
		if err := (&authtest.MockRevoker{}).Revoke(ctx, p); err != nil {
			t.Errorf("Revoke with no Fn = %v", err)
		}
	})
}
