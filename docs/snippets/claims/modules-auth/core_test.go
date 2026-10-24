package modulesauth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/migrate"
)

// Docs: modules/auth.mdx#what-it-is-for
// "`auth.Authenticator` turns an `*http.Request` into a `gas.Principal`. The
// `jwt`, `session` and `apikey` services implement it, and `auth.Chain`
// combines several. `token` does not"
// and "`auth.PrincipalRevoker` invalidates credentials: `Revoke` one,
// `RevokeAll` for a subject, `RevokeAllByScheme` for one scheme of a subject.
// `session` and `apikey` implement it."
// and "A JWT cannot be revoked: the service is not an `auth.PrincipalRevoker`"
func TestInterfaces(t *testing.T) {
	t.Parallel()
	authn := reflect.TypeFor[auth.Authenticator]()
	revoker := reflect.TypeFor[auth.PrincipalRevoker]()
	for _, tc := range []struct {
		typ            reflect.Type
		authn, revoker bool
	}{
		{reflect.TypeFor[*jwt.Service](), true, false},
		{reflect.TypeFor[*session.Service](), true, true},
		{reflect.TypeFor[*apikey.Service](), true, true},
		{reflect.TypeFor[*token.Service](), false, false},
		{reflect.TypeFor[auth.Chain](), true, false},
	} {
		if got := tc.typ.Implements(authn); got != tc.authn {
			t.Errorf("%v implements auth.Authenticator = %v, want %v", tc.typ, got, tc.authn)
		}
		if got := tc.typ.Implements(revoker); got != tc.revoker {
			t.Errorf("%v implements auth.PrincipalRevoker = %v, want %v", tc.typ, got, tc.revoker)
		}
	}
	for _, name := range []string{"Revoke", "RevokeAll", "RevokeAllByScheme"} {
		if _, ok := revoker.MethodByName(name); !ok {
			t.Errorf("auth.PrincipalRevoker has no %s", name)
		}
	}
}

// Docs: modules/auth.mdx#what-it-is-for
// "`auth.Authorizer` decides whether a principal may perform an action on a
// resource. The module ships no implementation; the app writes one, and
// `authtest` has a mock."
// Every exported type of the module's non-test packages other than authtest
// is checked.
func TestNoAuthorizerShipped(t *testing.T) {
	t.Parallel()
	authz := reflect.TypeFor[auth.Authorizer]()
	for _, typ := range []reflect.Type{
		reflect.TypeFor[*jwt.Service](), reflect.TypeFor[*session.Service](),
		reflect.TypeFor[*apikey.Service](), reflect.TypeFor[*token.Service](),
		reflect.TypeFor[auth.Chain](), reflect.TypeFor[*auth.BasePrincipal](),
	} {
		if typ.Implements(authz) {
			t.Errorf("%v implements auth.Authorizer", typ)
		}
	}
	if !reflect.TypeFor[*authtest.MockAuthorizer]().Implements(authz) {
		t.Error("authtest.MockAuthorizer does not implement auth.Authorizer")
	}
	// Any other implementation would need an Authorize method.
	err := filepath.WalkDir(authDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "authtest") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), ") Authorize(") {
			t.Errorf("%s declares an Authorize method", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// authDir is the gas/auth module, relative to this package.
const authDir = "../../../../auth"

// Docs: modules/auth.mdx#what-it-is-for
// "`gas.Principal` and the helpers that carry it on a context live in core, so
// a handler reads the principal with `ctx.Principal()` without importing this
// module."
// and
// Docs: modules/auth.mdx#reading-the-principal
// "`ctx.Principal()` returns the principal, or a 401 `*gas.Error` to return
// directly when there is none. Outside a handler,
// `gas.PrincipalFromContext(ctx)` returns it with a boolean."
func TestPrincipalFromCore(t *testing.T) {
	t.Parallel()
	p := auth.NewPrincipal("u1", auth.SchemeSession, "s1", nil)
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/me", func(ctx gas.Context) error {
		got, err := ctx.Principal()
		if err != nil {
			return err
		}
		return ctx.Text(http.StatusOK, got.Subject())
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	with := httptest.NewRequest(http.MethodGet, "/me", nil)
	with = with.WithContext(gas.WithPrincipal(with.Context(), p))
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, with)
	if rr.Code != 200 || rr.Body.String() != "u1" {
		t.Errorf("with a principal: %d %q, want 200 u1", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rr.Code != 401 {
		t.Errorf("without a principal: %d, want 401", rr.Code)
	}

	ctx := gas.NewContext(t.Context(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	_, err := ctx.Principal()
	if e, ok := gas.AsError(err); !ok || e.Status != 401 {
		t.Errorf("ctx.Principal() with none = %v, want a 401 *gas.Error", err)
	}
	if got, ok := gas.PrincipalFromContext(gas.WithPrincipal(t.Context(), p)); !ok || got != p {
		t.Errorf("PrincipalFromContext = %v, %v", got, ok)
	}
	if _, ok := gas.PrincipalFromContext(t.Context()); ok {
		t.Error("PrincipalFromContext with none = true")
	}
}

// params returns the parameter types of a constructor.
func params(ctor any) []reflect.Type {
	typ := reflect.TypeOf(ctor)
	var in []reflect.Type
	for i := range typ.NumIn() {
		in = append(in, typ.In(i))
	}
	return in
}

// Docs: modules/auth.mdx#wiring
// "Each sub-package's `New(opts...)` returns a constructor, and each service
// implements `gas.Service`. Register it under the sub-package's `Provider`
// interface (`jwt.Provider`, `session.Provider`, `apikey.Provider`,
// `token.Provider`) or as its `*Service`."
// and "| `jwt.New()` | `gas.ConfigProvider`, `gas.Logger` |"
// and "| `session.New()`, `apikey.New()`, `token.New()` | `gas.DatabaseProvider`, `gas.Logger`, `gas.MigrationManager`, `gas.ConfigProvider` |"
func TestConstructorsAndProviders(t *testing.T) {
	t.Parallel()
	var (
		_ gas.Service      = (*jwt.Service)(nil)
		_ gas.Service      = (*session.Service)(nil)
		_ gas.Service      = (*apikey.Service)(nil)
		_ gas.Service      = (*token.Service)(nil)
		_ jwt.Provider     = (*jwt.Service)(nil)
		_ session.Provider = (*session.Service)(nil)
		_ apikey.Provider  = (*apikey.Service)(nil)
		_ token.Provider   = (*token.Service)(nil)
	)
	cfgT, logT := reflect.TypeFor[gas.ConfigProvider](), reflect.TypeFor[gas.Logger]()
	dbT, migT := reflect.TypeFor[gas.DatabaseProvider](), reflect.TypeFor[gas.MigrationManager]()
	if got, want := params(jwt.New()), []reflect.Type{cfgT, logT}; !slices.Equal(got, want) {
		t.Errorf("jwt.New() parameters = %v, want %v", got, want)
	}
	for name, ctor := range map[string]any{"session": session.New(), "apikey": apikey.New(), "token": token.New()} {
		got := params(ctor)
		want := []reflect.Type{dbT, logT, migT, cfgT}
		if !slices.Equal(got, want) {
			t.Errorf("%s.New() parameters = %v, want %v", name, got, want)
		}
	}

	// Both registrations resolve in a running app.
	for _, asProvider := range []bool{true, false} {
		db := sqliteDB(t)
		opts := []gas.Option{
			gas.WithServiceInstance[gas.ConfigProvider](boundConfig(t, map[string]any{"jwt": map[string]any{"signingkey": signingKey}})),
			gas.WithServiceInstance[gas.Logger](nopLogger()),
			gas.WithServiceInstance[gas.DatabaseProvider](gas.DatabaseProvider(db)),
			gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		}
		if asProvider {
			opts = append(opts,
				gas.WithSingletonService[jwt.Provider](func(c gas.ConfigProvider, l gas.Logger) (jwt.Provider, error) { return jwt.New()(c, l) }),
				gas.WithSingletonService[session.Provider](func(d gas.DatabaseProvider, l gas.Logger, m gas.MigrationManager, c gas.ConfigProvider) (session.Provider, error) {
					return session.New()(d, l, m, c)
				}),
				gas.WithSingletonService[apikey.Provider](func(d gas.DatabaseProvider, l gas.Logger, m gas.MigrationManager, c gas.ConfigProvider) (apikey.Provider, error) {
					return apikey.New()(d, l, m, c)
				}),
				gas.WithSingletonService[token.Provider](func(d gas.DatabaseProvider, l gas.Logger, m gas.MigrationManager, c gas.ConfigProvider) (token.Provider, error) {
					return token.New()(d, l, m, c)
				}),
			)
		} else {
			opts = append(opts,
				gas.WithSingletonService[*jwt.Service](jwt.New()),
				gas.WithSingletonService[*session.Service](session.New()),
				gas.WithSingletonService[*apikey.Service](apikey.New()),
				gas.WithSingletonService[*token.Service](token.New()),
			)
		}
		w := gas.NewWorker(opts...)
		err := w.Start(t.Context())
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if err != nil {
			t.Fatalf("Start with registration as Provider=%v: %v", asProvider, err)
		}
		for _, table := range []string{"__gas_auth_sessions", "__gas_auth_api_keys", "__gas_auth_tokens"} {
			_ = rowCount(t, db.DB(), table)
		}
	}
}

// Docs: modules/auth.mdx#wiring
// "`session`, `apikey` and `token` also pick a SQL dialect from the database
// provider's `Driver()` and register their migrations with the
// `gas.MigrationManager` in `Init`. Core runs the migrations after every
// service's `Init`, before the app starts serving."
func TestStoresRegisterMigrationsInInit(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]int{"session": 1, "apikey": 2, "token": 1} {
		db := sqliteDB(t)
		mig := newMigrations(t, db)
		var svc gas.Service
		var err error
		switch kind {
		case "session":
			svc, err = session.New()(db, nopLogger(), mig, boundConfig(t, map[string]any{}))
		case "apikey":
			svc, err = apikey.New()(db, nopLogger(), mig, boundConfig(t, map[string]any{}))
		case "token":
			svc, err = token.New()(db, nopLogger(), mig, boundConfig(t, map[string]any{}))
		}
		if err != nil {
			t.Fatalf("%s.New: %v", kind, err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := mig.CheckReady(t.Context()); err != nil {
			t.Errorf("%s: pending before Init = %v, want none", kind, err)
		}
		if err := svc.Init(t.Context()); err != nil {
			t.Fatalf("%s Init: %v", kind, err)
		}
		if err := mig.CheckReady(t.Context()); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d pending migration(s)", want)) {
			t.Errorf("%s: after Init CheckReady = %v, want %d pending", kind, err, want)
		}
	}

	db := sqliteDB(t)
	var tableAtReady bool
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](boundConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithServiceInstance[gas.DatabaseProvider](gas.DatabaseProvider(db)),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*session.Service](session.New()),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			var n int
			tableAtReady = db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM __gas_auth_sessions").Scan(&n) == nil
			return nil
		}),
	)
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !tableAtReady {
		t.Error("__gas_auth_sessions did not exist when the ready hooks ran")
	}
}

// Docs: modules/auth.mdx#protecting-routes
// "`auth.Middleware(a, opts...)` runs the authenticator on each request. On
// success it stores the principal on the request context with
// `gas.WithPrincipal` and calls the next handler. On failure it calls the
// `WithOnError` handler with the error; without one it answers a plain-text
// `401 Unauthorized`."
func TestMiddleware(t *testing.T) {
	t.Parallel()
	p := auth.NewPrincipal("u1", auth.SchemeJWT, "j1", nil)
	m := fixed(p)
	_, got, reached := serve(auth.Middleware(m), httptest.NewRequest(http.MethodGet, "/", nil))
	_, _, _ = serve(auth.Middleware(m), httptest.NewRequest(http.MethodGet, "/", nil))
	if !reached || got != p {
		t.Errorf("success: reached %v with principal %v, want the authenticator's", reached, got)
	}
	if m.CallCount("Authenticate") != 2 {
		t.Errorf("Authenticate calls = %d, want one per request", m.CallCount("Authenticate"))
	}

	rec, _, reached := serve(auth.Middleware(failing(auth.ErrUnauthenticated)), httptest.NewRequest(http.MethodGet, "/", nil))
	if reached || rec.Code != 401 || rec.Body.String() != "Unauthorized\n" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("default failure = reached %v, %d %q %q; want a plain-text 401 Unauthorized", reached, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	sentinel := errors.New("custom failure")
	var seen error
	onErr := auth.WithOnError(func(w http.ResponseWriter, _ *http.Request, err error) {
		seen = err
		w.WriteHeader(http.StatusTeapot)
	})
	rec, _, reached = serve(auth.Middleware(failing(sentinel), onErr), httptest.NewRequest(http.MethodGet, "/", nil))
	if reached || rec.Code != http.StatusTeapot || seen != sentinel { //nolint:errorlint // passed through unchanged
		t.Errorf("WithOnError: reached %v, %d, saw %v; want the handler's response and the error itself", reached, rec.Code, seen)
	}
}

// Docs: modules/auth.mdx#protecting-routes
// "An authenticator that returns a `nil` principal with a `nil` error has
// failed with `auth.ErrUnauthenticated`; it never counts as a success. Passing
// a `nil` authenticator panics when the middleware is built."
func TestMiddlewareNilCases(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]gas.Principal{"nil": nil, "typed nil": (*auth.BasePrincipal)(nil)} {
		var seen error
		onErr := auth.WithOnError(func(w http.ResponseWriter, _ *http.Request, err error) {
			seen = err
			w.WriteHeader(401)
		})
		_, _, reached := serve(auth.Middleware(fixed(p), onErr), httptest.NewRequest(http.MethodGet, "/", nil))
		if reached || !errors.Is(seen, auth.ErrUnauthenticated) {
			t.Errorf("%s principal: reached %v, error %v; want ErrUnauthenticated", name, reached, seen)
		}
	}
	var built bool
	func() {
		defer func() { _ = recover() }()
		_ = auth.Middleware(nil)
		built = true
	}()
	if built {
		t.Error("auth.Middleware(nil) did not panic")
	}
}

// Docs: modules/auth.mdx#protecting-routes
// "`auth.Chain{...}` tries each authenticator in order and returns the first
// success. When all fail it returns the last one's error, so with
// `Chain{jwt, session, apikey}` the error a handler sees comes from `apikey`.
// An empty chain returns `auth.ErrUnauthenticated`."
func TestChain(t *testing.T) {
	t.Parallel()
	p1 := auth.NewPrincipal("a", auth.SchemeJWT, "", nil)
	p2 := auth.NewPrincipal("b", auth.SchemeSession, "", nil)
	first, second := fixed(p1), fixed(p2)
	errA, errB := errors.New("a"), errors.New("b")
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if got, err := (auth.Chain{failing(errA), first, second}).Authenticate(t.Context(), r); err != nil || got != p1 {
		t.Errorf("Chain = %v, %v; want the first success", got, err)
	}
	if second.CallCount("Authenticate") != 0 {
		t.Error("Chain called an authenticator after the first success")
	}
	if _, err := (auth.Chain{failing(errA), failing(errB)}).Authenticate(t.Context(), r); err != errB { //nolint:errorlint // the last error itself
		t.Errorf("Chain with all failing = %v, want the last error", err)
	}
	if _, err := (auth.Chain{}).Authenticate(t.Context(), r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("empty Chain = %v, want ErrUnauthenticated", err)
	}

	// The page's example: an expired JWT and a missing key give the apikey
	// error.
	j := mustJWT(t, hsConfig())
	expired, err := j.SignWithExpiry("u", nil, -1)
	if err != nil {
		t.Fatalf("SignWithExpiry: %v", err)
	}
	db := sqliteDB(t)
	_, err = (auth.Chain{j, mustSession(t, db, nil, nil), mustAPIKey(t, db, nil)}).Authenticate(t.Context(), bearer(expired))
	if !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrCredentialsExpired) {
		t.Errorf("Chain{jwt, session, apikey} with only an expired JWT = %v, want apikey's ErrUnauthenticated", err)
	}
}

// Docs: modules/auth.mdx#protecting-routes
// "`auth.RequireScheme(scheme)` answers a plain-text `403 Forbidden` when the
// request has no principal or one of another scheme."
func TestRequireScheme(t *testing.T) {
	t.Parallel()
	mw := auth.RequireScheme(auth.SchemeSession)
	for name, p := range map[string]gas.Principal{
		"none":    nil,
		"other":   auth.NewPrincipal("u", auth.SchemeAPIKey, "", nil),
		"matches": auth.NewPrincipal("u", auth.SchemeSession, "", nil),
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if p != nil {
			r = r.WithContext(gas.WithPrincipal(r.Context(), p))
		}
		rec, _, reached := serve(mw, r)
		if name == "matches" {
			if !reached {
				t.Errorf("matching scheme was refused: %d", rec.Code)
			}
			continue
		}
		if reached || rec.Code != 403 || rec.Body.String() != "Forbidden\n" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s: reached %v, %d %q; want a plain-text 403 Forbidden", name, reached, rec.Code, rec.Body.String())
		}
	}
}

// Docs: modules/auth.mdx#reading-the-principal
// "A `gas.Principal` has `Subject()` (the stable user ID), `Scheme()` (`jwt`,
// `session` or `apikey`, also available as `auth.SchemeJWT`,
// `auth.SchemeSession` and `auth.SchemeAPIKey`), `CredentialID()` (the session
// ID, API key ID or JWT `jti`) and `Metadata()`."
// and "`gas.MetadataValue[T](p.Metadata(), key)` reads a metadata value and
// reports `false` when the key is missing or holds another type"
func TestPrincipalFields(t *testing.T) {
	t.Parallel()
	if auth.SchemeJWT != "jwt" || auth.SchemeSession != "session" || auth.SchemeAPIKey != "apikey" {
		t.Errorf("schemes = %q %q %q", auth.SchemeJWT, auth.SchemeSession, auth.SchemeAPIKey)
	}
	db := sqliteDB(t)
	sess := mustSession(t, db, nil, nil)
	r, s := sessionRequest(t, sess, "u1", gas.BasePrincipalMetadata{"plan": "pro"})
	sp, err := sess.Authenticate(t.Context(), r)
	if err != nil || sp.Subject() != "u1" || sp.Scheme() != "session" || sp.CredentialID() != s.ID {
		t.Errorf("session principal = %v, %v", sp, err)
	}
	keys := mustAPIKey(t, db, nil)
	key, info := generate(t, keys, "svc", nil)
	kp, err := keys.Authenticate(t.Context(), apiKeyRequest(key))
	if err != nil || kp.Scheme() != "apikey" || kp.CredentialID() != info.ID {
		t.Errorf("apikey principal = %v, %v", kp, err)
	}
	j := mustJWT(t, hsConfig())
	tok, _ := j.Sign("u2", map[string]any{"jti": "tok-1"})
	jp, err := j.Authenticate(t.Context(), bearer(tok))
	if err != nil || jp.Scheme() != "jwt" || jp.CredentialID() != "tok-1" {
		t.Errorf("jwt principal = %v, %v", jp, err)
	}

	if v, ok := gas.MetadataValue[string](sp.Metadata(), "plan"); !ok || v != "pro" {
		t.Errorf("MetadataValue[string](plan) = %q, %v", v, ok)
	}
	if _, ok := gas.MetadataValue[int](sp.Metadata(), "plan"); ok {
		t.Error("MetadataValue[int] of a string = true")
	}
	if _, ok := gas.MetadataValue[string](sp.Metadata(), "missing"); ok {
		t.Error("MetadataValue of a missing key = true")
	}
}

// Docs: modules/auth.mdx#error-responses
// "`WithOnError(fn)` receives the authenticator's error and owns the response.
// The errors are passed through unchanged, so `errors.Is` matches the
// sentinels."
func TestOnErrorSeesSentinels(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	j := mustJWT(t, hsConfig())
	expired, _ := j.SignWithExpiry("u", nil, -1)
	for name, tc := range map[string]struct {
		a    auth.Authenticator
		r    *http.Request
		want error
	}{
		"jwt expired":    {j, bearer(expired), auth.ErrCredentialsExpired},
		"session absent": {mustSession(t, db, nil, nil), httptest.NewRequest(http.MethodGet, "/", nil), auth.ErrUnauthenticated},
		"apikey unknown": {mustAPIKey(t, db, nil), apiKeyRequest("nope"), auth.ErrUnauthenticated},
	} {
		var seen error
		rec, _, _ := serve(auth.Middleware(tc.a, auth.WithOnError(func(w http.ResponseWriter, _ *http.Request, err error) {
			seen = err
			_ = gas.WriteError(w, httptest.NewRequest(http.MethodGet, "/", nil), gas.Unauthorized("no"))
		})), tc.r)
		if !errors.Is(seen, tc.want) || rec.Code != 401 {
			t.Errorf("%s: handler saw %v (status %d), want errors.Is %v", name, seen, rec.Code, tc.want)
		}
	}
}

// Docs: modules/auth.mdx#errors
// "| `auth.ErrForbidden`, `auth.ErrCredentialRevoked` | Declared for your own code, such as an `Authorizer`. No service in the module returns them. |"
func TestUnusedSentinels(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(authDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"ErrForbidden", "ErrCredentialRevoked"} {
			uses := strings.Count(string(src), name)
			if filepath.Base(path) == "auth.go" && filepath.Dir(path) == filepath.Clean(authDir) {
				uses -= 2 // its declaration and doc comment
			}
			if uses > 0 {
				t.Errorf("%s uses %s", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// Docs: modules/auth.mdx#errors
// "The session and API key services wrap store lookups, so check with
// `errors.Is` rather than `==`."
func TestStoreLookupsWrapped(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "session_id", Value: "unknown"})
	_, sErr := mustSession(t, db, nil, nil).Authenticate(t.Context(), r)
	_, kErr := mustAPIKey(t, db, nil).Authenticate(t.Context(), apiKeyRequest("unknown"))
	for name, err := range map[string]error{"session": sErr, "apikey": kErr} {
		if !errors.Is(err, auth.ErrUnauthenticated) || err == auth.ErrUnauthenticated { //nolint:errorlint // checking that it is wrapped
			t.Errorf("%s unknown credential = %v, want ErrUnauthenticated wrapped", name, err)
		}
	}
}

// Docs: modules/auth.mdx#testing
// "`authtest` has a recording mock for each interface. Each records its calls
// in `Calls`, has `CallCount(method)` and `Reset()`, and delegates to an `Fn`
// field when set:"
// and "| `MockAuthenticator` | `Authenticate` returns `auth.ErrUnauthenticated`. |"
// and "| `MockAuthorizer` | `Authorize` returns `nil`, allowing everything. |"
// and "| `MockRevoker` | Every method returns `nil`. |"
func TestMocks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	p := auth.NewPrincipal("u1", auth.SchemeSession, "s", nil)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	boom := errors.New("boom")

	a := &authtest.MockAuthenticator{}
	if _, err := a.Authenticate(ctx, r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("MockAuthenticator default = %v", err)
	}
	a.AuthenticateFn = func(context.Context, *http.Request) (gas.Principal, error) { return p, nil }
	if got, err := a.Authenticate(ctx, r); err != nil || got != p {
		t.Errorf("MockAuthenticator with Fn = %v, %v", got, err)
	}
	if a.CallCount("Authenticate") != 2 || len(a.Calls) != 2 {
		t.Errorf("MockAuthenticator calls = %v", a.Calls)
	}
	a.Reset()
	if len(a.Calls) != 0 {
		t.Error("MockAuthenticator Reset did not clear Calls")
	}

	z := &authtest.MockAuthorizer{}
	if err := z.Authorize(ctx, p, "delete", "everything"); err != nil {
		t.Errorf("MockAuthorizer default = %v", err)
	}
	z.AuthorizeFn = func(context.Context, gas.Principal, string, string) error { return boom }
	if err := z.Authorize(ctx, p, "a", "b"); err != boom { //nolint:errorlint // delegated as is
		t.Errorf("MockAuthorizer with Fn = %v", err)
	}
	if z.CallCount("Authorize") != 2 {
		t.Errorf("MockAuthorizer calls = %v", z.Calls)
	}
	z.Reset()
	if len(z.Calls) != 0 {
		t.Error("MockAuthorizer Reset did not clear Calls")
	}

	v := &authtest.MockRevoker{}
	for name, err := range map[string]error{
		"Revoke":            v.Revoke(ctx, p),
		"RevokeAll":         v.RevokeAll(ctx, "u1"),
		"RevokeAllByScheme": v.RevokeAllByScheme(ctx, "u1", "session"),
	} {
		if err != nil {
			t.Errorf("MockRevoker default %s = %v", name, err)
		}
	}
	v.RevokeFn = func(context.Context, gas.Principal) error { return boom }
	v.RevokeAllFn = func(context.Context, string) error { return boom }
	v.RevokeAllBySchemeFn = func(context.Context, string, string) error { return boom }
	for name, err := range map[string]error{
		"Revoke":            v.Revoke(ctx, p),
		"RevokeAll":         v.RevokeAll(ctx, "u1"),
		"RevokeAllByScheme": v.RevokeAllByScheme(ctx, "u1", "session"),
	} {
		if err != boom { //nolint:errorlint // delegated as is
			t.Errorf("MockRevoker with Fn %s = %v", name, err)
		}
		if v.CallCount(name) != 2 {
			t.Errorf("MockRevoker CallCount(%s) = %d, want 2", name, v.CallCount(name))
		}
	}
	v.Reset()
	if len(v.Calls) != 0 {
		t.Error("MockRevoker Reset did not clear Calls")
	}
}

// Docs: modules/auth.mdx#testing
// "`auth.NewPrincipal(subject, scheme, credentialID, meta)` builds the
// principal a mock returns; a `nil` metadata becomes an empty map."
func TestNewPrincipal(t *testing.T) {
	t.Parallel()
	p := auth.NewPrincipal("u", "custom", "c", nil)
	if p.Subject() != "u" || p.Scheme() != "custom" || p.CredentialID() != "c" {
		t.Errorf("NewPrincipal fields = %q %q %q", p.Subject(), p.Scheme(), p.CredentialID())
	}
	m, ok := p.Metadata().(gas.BasePrincipalMetadata)
	if !ok || m == nil || len(m) != 0 {
		t.Errorf("Metadata() with nil meta = %#v, want an empty map", p.Metadata())
	}
}
