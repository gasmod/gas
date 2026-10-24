package starttutorial_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/app"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/keys"
	"github.com/gasmod/gas/docs/snippets/start-tutorial/notes"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
)

// jsonKeys returns the keys of the JSON object body, in order.
func jsonKeys(t *testing.T, body string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("body %q is not a JSON object", body)
	}
	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`POST /keys` creates a key for a subject, and only callers that present
// the admin token from the config may use it."
// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "A returned `*gas.Error`, such as the one `gas.Unauthorized` builds, is
// written as a JSON error at its status."
// Docs: start/tutorial.mdx#run-it-2
// "A wrong token gets a JSON error:" and
// "curl -X POST localhost:8080/keys -H 'X-Admin-Token: wrong' -d '{}'" and
// "{ "error": { "status": 401, "code": "unauthorized", "message": "a valid
// X-Admin-Token header is required" } }"
func TestKeysNeedTheAdminToken(t *testing.T) {
	_, srv := fullApp(t, nil)
	const want = `{"error":{"status":401,"code":"unauthorized","message":"a valid X-Admin-Token header is required"}}`
	for name, headers := range map[string]map[string]string{
		"no token":    nil,
		"wrong token": {"X-Admin-Token": "wrong"},
	} {
		r := curl(t, srv, http.MethodPost, "/keys", `{}`, headers)
		if r.status != http.StatusUnauthorized || strings.TrimSpace(r.body) != want ||
			!strings.HasPrefix(r.ctype, "application/json") {
			t.Errorf("%s: %d %s %q, want 401 application/json %s", name, r.status, r.ctype, r.body, want)
		}
	}
	if r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`, admin); r.status != http.StatusCreated {
		t.Errorf("with the config's token: %d %s, want 201", r.status, r.body)
	}
}

// Docs: start/tutorial.mdx#run-it-2
// "curl -X POST localhost:8080/keys \ -H 'X-Admin-Token:
// change-me-to-a-long-random-string' \ -d '{"subject":"alice","name":"laptop"}'"
// and "{"id":"aea43e9f-3da1-435c-861d-ea7ed68aba45","subject":"alice","name":"laptop","key":"1cGmFmGFeQzs9S1W0zfAeCAuY3TdyNN_FUfGknLKFKY"}"
// and "Your `id` and `key` will differ."
// The id and key vary per run, so this checks the field order, the stable
// values, and that id is a UUID and key is URL-safe base64 text.
func TestCreateKeyResponseShape(t *testing.T) {
	_, srv := fullApp(t, nil)
	r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`, admin)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /keys: %d %s", r.status, r.body)
	}
	if got := jsonKeys(t, r.body); !slices.Equal(got, []string{"id", "subject", "name", "key"}) {
		t.Errorf("fields %v, want [id subject name key]", got)
	}
	var out struct{ ID, Subject, Name, Key string }
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatal(err)
	}
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !uuid.MatchString(out.ID) || out.Subject != "alice" || out.Name != "laptop" ||
		!regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(out.Key) {
		t.Fatalf("response %+v, want a UUID id, subject alice, name laptop, and a URL-safe key", out)
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`Name` identifies the service in logs and in the route map, and marks it
// as the owner of its routes. `Init` runs once at startup; this one binds its
// part of the config and registers its route. `Close` runs at shutdown"
func TestServiceNameOwnsRoutesAndNamesLogs(t *testing.T) {
	sink := captureSlog(t)
	project(t, pageConfig(t))
	a := app.New(loadConfig(t))
	if got := a.Router().Routes()["keys"]; len(got) != 0 {
		t.Errorf("keys routes before Start: %v, want none until Init", got)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	routes := a.Router().Routes()
	if got := routes["keys"]; len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/keys" {
		t.Errorf("routes owned by keys: %v, want POST /keys once", got)
	}
	if got := routes["notes"]; len(got) != 4 {
		t.Errorf("routes owned by notes: %v, want 4", got)
	}
	if len(sink.find(t, "closing service", map[string]string{"service": "keys"})) != 0 {
		t.Error("keys closed before shutdown")
	}
	if err := a.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keys", "notes"} {
		if n := len(sink.find(t, "closing service", map[string]string{"service": name})); n != 1 {
			t.Errorf("closing service records naming %s: %d, want 1", name, n)
		}
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`Bind` fills `Config` from the loaded config and checks its `validate`
// tags. A missing admin token, or one shorter than 16 characters, stops
// startup:" and "building *keys.Service: init *keys.Service: keys: config:
// validate config: Key: 'Config.Keys.AdminToken' Error:Field validation for
// 'AdminToken' failed on the 'min' tag"
func TestAdminTokenIsValidated(t *testing.T) {
	const prefix = "building *keys.Service: init *keys.Service: keys: config: validate config: " +
		"Key: 'Config.Keys.AdminToken' Error:Field validation for 'AdminToken' failed on the "
	for _, c := range []struct {
		name  string
		token any // nil drops the key
		tag   string
		ok    bool
	}{
		{"missing", nil, "'required' tag", false},
		{"15 characters", strings.Repeat("x", 15), "'min' tag", false},
		{"16 characters", strings.Repeat("x", 16), "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := pageConfig(t)
			if c.token == nil {
				delete(section(cfg, "Keys"), "AdminToken")
			} else {
				section(cfg, "Keys")["AdminToken"] = c.token
			}
			project(t, cfg)
			a := app.New(loadConfig(t))
			err := a.Start(t.Context())
			if err == nil {
				_ = a.Stop(context.Background())
			}
			switch {
			case c.ok && err != nil:
				t.Fatalf("Start: %v", err)
			case !c.ok && err == nil:
				t.Fatal("Start succeeded")
			case !c.ok && !strings.Contains(err.Error(), prefix+c.tag):
				t.Fatalf("Start error:\n%v\nwant it to contain:\n%s", err, prefix+c.tag)
			}
		})
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`BindJSON` decodes the body and runs the `validate` tags."
func TestBindJSONDecodesAndValidates(t *testing.T) {
	_, srv := fullApp(t, nil)
	r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice"}`, admin)
	const want = `{"error":{"status":422,"code":"validation_failed","message":"request validation failed",` +
		`"fields":[{"field":"name","rule":"required","message":"is required"}]}}`
	if r.status != http.StatusUnprocessableEntity || strings.TrimSpace(r.body) != want {
		t.Errorf("missing name: %d %s, want 422 %s", r.status, r.body, want)
	}
	if r := curl(t, srv, http.MethodPost, "/keys", `{"subject":`, admin); r.status != http.StatusBadRequest {
		t.Errorf("malformed body: %d %s, want 400", r.status, r.body)
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`Generate` returns the plaintext key once; the database keeps its SHA-256
// hash and its first 8 characters, not the key."
func TestKeyIsStoredAsHashAndPrefix(t *testing.T) {
	a, srv := fullApp(t, nil)
	key := issueKey(t, srv, "alice")

	rows, err := dbOf(t, a).QueryContext(t.Context(), `SELECT * FROM __gas_auth_api_keys`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, c := range cols {
			row[c] = vals[i].String
			if strings.Contains(vals[i].String, key) {
				t.Errorf("column %s holds the plaintext key", c)
			}
		}
		sum := sha256.Sum256([]byte(key))
		if row["key_hash"] != hex.EncodeToString(sum[:]) {
			t.Errorf("key_hash = %q, want the key's SHA-256", row["key_hash"])
		}
		if row["key_prefix"] != key[:8] {
			t.Errorf("key_prefix = %q, want the key's first 8 characters %q", row["key_prefix"], key[:8])
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d stored keys, want 1", n)
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "`apikey.New()` takes a `gas.DatabaseProvider`, `gas.Logger`,
// `gas.MigrationManager`, and `gas.ConfigProvider` from the container."
// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "It is registered under the `apikey.Provider` interface, and the services
// that use it ask for that type."
func TestAPIKeyConstructorDependencies(t *testing.T) {
	ctor := reflect.TypeOf(apikey.New())
	var in []reflect.Type
	for i := range ctor.NumIn() {
		in = append(in, ctor.In(i))
	}
	want := []reflect.Type{
		reflect.TypeFor[gas.DatabaseProvider](), reflect.TypeFor[gas.Logger](),
		reflect.TypeFor[gas.MigrationManager](), reflect.TypeFor[gas.ConfigProvider](),
	}
	if !slices.Equal(in, want) {
		t.Errorf("apikey.New() takes %v, want %v", in, want)
	}
	provider := reflect.TypeFor[apikey.Provider]()
	for name, fn := range map[string]any{"keys.New": keys.New, "notes.New": notes.New} {
		ft := reflect.TypeOf(fn)
		found := false
		for i := range ft.NumIn() {
			found = found || ft.In(i) == provider
		}
		if !found {
			t.Errorf("%s does not take an apikey.Provider", name)
		}
	}
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "In `Init` it reads the `APIKey` section of the config (nothing to set here:
// the header defaults to `X-API-Key`)"
// The header key is APIKey.HeaderName, which the page does not name.
func TestAPIKeyHeaderDefaultAndSection(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		_, srv := fullApp(t, nil)
		key := issueKey(t, srv, "alice")
		if r := curl(t, srv, http.MethodGet, "/notes", "", map[string]string{"X-API-Key": key}); r.status != http.StatusOK {
			t.Fatalf("X-API-Key: %d %s, want 200", r.status, r.body)
		}
	})
	t.Run("configured", func(t *testing.T) {
		_, srv := fullApp(t, func(cfg map[string]any) {
			section(cfg, "APIKey")["HeaderName"] = "X-Token"
		})
		key := issueKey(t, srv, "alice")
		if r := curl(t, srv, http.MethodGet, "/notes", "", map[string]string{"X-Token": key}); r.status != http.StatusOK {
			t.Errorf("X-Token: %d %s, want 200", r.status, r.body)
		}
		if r := curl(t, srv, http.MethodGet, "/notes", "", map[string]string{"X-API-Key": key}); r.status != http.StatusUnauthorized {
			t.Errorf("X-API-Key with APIKey.HeaderName set: %d, want 401", r.status)
		}
	})
}

// Docs: start/tutorial.mdx#step-4-issue-api-keys
// "and registers its own migrations for the configured driver, which create
// its table."
func TestAPIKeyMigrationsCreateItsTable(t *testing.T) {
	project(t, pageConfig(t))
	a := step4App(loadConfig(t))
	start(t, a)
	if got := tables(t, dbOf(t, a)); !slices.Contains(got, "__gas_auth_api_keys") {
		t.Fatalf("tables %v, want the API key table", got)
	}
}

// routeMap returns the message of the route map record, each line's
// trailing spaces trimmed.
func routeMap(t *testing.T, sink *logSink) string {
	t.Helper()
	for _, r := range sink.records(t) {
		msg, _ := r["msg"].(string)
		if strings.HasPrefix(msg, "route map") {
			if r["level"] != "INFO" {
				t.Errorf("route map level %v, want INFO", r["level"])
			}
			lines := strings.Split(msg, "\n")
			for i, l := range lines {
				lines[i] = strings.TrimRight(l, " ")
			}
			return strings.Join(lines, "\n")
		}
	}
	return ""
}

// Docs: start/tutorial.mdx#run-it-2
// "`Run` logs the route map at startup. With `GasEnv` set to `development`, it
// is a table:" and "... INFO SQL database connection initialized
// service=gas/database ... INFO service initialized service=gas/auth/apikey
// ... INFO applying pending migrations ... INFO route map (1 routes across 1
// services): ───────────────── [keys]: POST /keys ───────────────── ... INFO
// server started environment=development addr=localhost:8080"
// The port is a free one instead of 8080.
func TestRunStep4RouteMap(t *testing.T) {
	sink := captureSlog(t)
	port := freePort(t)
	project(t, withPort(t, port))
	r := runApp(t, step4App(loadConfig(t)), port)
	if err := r.stop(syscall.SIGINT); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = "route map (1 routes across 1 services):\n" +
		"─────────────────\n" +
		"  [keys]:\n" +
		"      POST  /keys\n" +
		"─────────────────"
	if got := routeMap(t, sink); got != want {
		t.Errorf("route map:\n%s\nwant:\n%s", got, want)
	}
	routeAt := -1
	for i, rec := range sink.records(t) {
		if msg, _ := rec["msg"].(string); strings.HasPrefix(msg, "route map") {
			routeAt = i
		}
	}
	got := []int{
		sink.index(t, "SQL database connection initialized", map[string]string{"service": "gas/database"}),
		sink.index(t, "service initialized", map[string]string{"service": "gas/auth/apikey"}),
		sink.index(t, "applying pending migrations", nil),
		routeAt,
		sink.index(t, "server started", map[string]string{"environment": "development", "addr": "localhost:" + strconv.Itoa(port)}),
	}
	if slices.Contains(got, -1) || !slices.IsSorted(got) {
		t.Fatalf("record positions %v, want all present and in the page's order", got)
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "A migration file is named `{version}_{description}.up.sql`, and every
// `.up.sql` needs a matching `.down.sql`. The version is everything before the
// first underscore."
func TestMigrationFileNaming(t *testing.T) {
	t.Run("down file required", func(t *testing.T) {
		mm, err := migrate.New()(nil)
		if err != nil {
			t.Fatal(err)
		}
		err = mm.RegisterFS(&probe[kFirst]{}, fstest.MapFS{
			"20261021001_create_notes.up.sql": {Data: []byte("CREATE TABLE x (v TEXT)")},
		})
		if err == nil {
			t.Fatal("RegisterFS accepted an .up.sql without its .down.sql")
		}
	})
	t.Run("version before first underscore", func(t *testing.T) {
		project(t, pageConfig(t))
		a := step3App(loadConfig(t),
			gas.WithSingletonService[*probe[kFirst]](func(mm gas.MigrationManager, db gas.DatabaseProvider) *probe[kFirst] {
				return &probe[kFirst]{mm: mm, db: db, files: migrationFiles("20261021_001_create_x",
					"CREATE TABLE x (v TEXT)", "DROP TABLE x")}
			}),
		)
		start(t, a)
		var v string
		if err := dbOf(t, a).QueryRowContext(t.Context(), `SELECT version FROM __gas_migrations`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != "20261021" {
			t.Fatalf("version %q, want 20261021", v)
		}
	})
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "Because `New` takes an `apikey.Provider`, the container builds the API key
// service before this one."
// Read as: the API key service is built and initialized before the notes
// constructor runs, even when notes is registered first.
func TestAPIKeyServiceIsBuiltBeforeNotes(t *testing.T) {
	sink := captureSlog(t)
	project(t, pageConfig(t))
	var apikeyFirst, built bool
	a := gas.NewApp(
		gas.WithSingletonService[*notes.Service](func(
			r *gas.Router, db gas.DatabaseProvider, mm gas.MigrationManager, p apikey.Provider, l gas.Logger,
		) *notes.Service {
			built = true
			apikeyFirst = len(sink.find(t, "service initialized", map[string]string{"service": "gas/auth/apikey"})) == 1
			return notes.New(r, db, mm, p, l)
		}),
		gas.WithServiceInstance[gas.ConfigProvider](loadConfig(t)),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[apikey.Provider](apikey.New()),
	)
	start(t, a)
	if !built || !apikeyFirst {
		t.Fatalf("notes built=%v, API key service initialized first=%v; want both", built, apikeyFirst)
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "`RegisterFS` reads the embedded files and registers them as migrations
// owned by this service"
// Docs: start/tutorial.mdx#run-it-3
// "With `sqlite3`, the tracking table shows the migrations from both services
// in version order:" and "sqlite3 notes.db 'SELECT version, service FROM
// __gas_migrations ORDER BY version'" and "20250323002|gas/auth/apikey
// 20260415042824|gas/auth/apikey 20261021001|notes"
func TestTrackingTableRows(t *testing.T) {
	a, _ := fullApp(t, nil)
	rows, err := dbOf(t, a).QueryContext(t.Context(), `SELECT version, service FROM __gas_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var v, s string
		if err := rows.Scan(&v, &s); err != nil {
			t.Fatal(err)
		}
		got = append(got, v+"|"+s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"20250323002|gas/auth/apikey", "20260415042824|gas/auth/apikey", "20261021001|notes"}
	if !slices.Equal(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	if !slices.Contains(tables(t, dbOf(t, a)), "notes") {
		t.Fatal("the notes migration did not create the notes table")
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "`auth.Middleware` authenticates each request with the API key provider,
// which reads the key from the `X-API-Key` header. On success it puts the
// principal on the request context and calls the handler. On failure it calls
// the `WithOnError` function, which here writes the same JSON error shape the
// handlers produce; without `WithOnError`, the middleware writes a plain-text
// 401."
func TestAuthMiddleware(t *testing.T) {
	a, srv := fullApp(t, nil)
	key := issueKey(t, srv, "alice")

	// The notes routes: the JSON shape of the keys handler's own 401.
	notesErr := curl(t, srv, http.MethodGet, "/notes", "", map[string]string{"X-API-Key": "not-a-key"})
	keysErr := curl(t, srv, http.MethodPost, "/keys", `{}`, nil)
	var n, k map[string]map[string]any
	if err := json.Unmarshal([]byte(notesErr.body), &n); err != nil || notesErr.status != http.StatusUnauthorized {
		t.Fatalf("bad key on /notes: %d %q (%v), want a JSON 401", notesErr.status, notesErr.body, err)
	}
	if err := json.Unmarshal([]byte(keysErr.body), &k); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(maps.Keys(n["error"])), slices.Sorted(maps.Keys(k["error"]))) ||
		n["error"]["status"] != k["error"]["status"] || n["error"]["code"] != k["error"]["code"] {
		t.Errorf("notes 401 %s and handler 401 %s differ in shape", notesErr.body, keysErr.body)
	}

	// The same provider without WithOnError.
	p, err := a.ServiceContainer().Resolve[apikey.Provider]()
	if err != nil {
		t.Fatal(err)
	}
	var subject string
	bare := httptest.NewServer(auth.Middleware(p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pr, ok := gas.PrincipalFromContext(r.Context()); ok {
			subject = pr.Subject()
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	t.Cleanup(bare.Close)
	r := curl(t, bare, http.MethodGet, "/", "", nil)
	if r.status != http.StatusUnauthorized || !strings.HasPrefix(r.ctype, "text/plain") {
		t.Errorf("without WithOnError: %d %s %q, want a plain-text 401", r.status, r.ctype, r.body)
	}
	r = curl(t, bare, http.MethodGet, "/", "", map[string]string{"X-API-Key": key})
	if r.status != http.StatusNoContent || subject != "alice" {
		t.Errorf("with a key: %d, principal subject %q; want the handler called with alice", r.status, subject)
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "The middleware is added inside `Route(" and "so it applies to the notes
// routes and not to `POST /keys`."
// Docs: start/tutorial.mdx#run-it-3
// "Without a key, the notes routes answer 401:" and "curl
// localhost:8080/notes" and
// "{ "error": { "status": 401, "code": "unauthorized", "message": "a valid
// X-API-Key header is required" } }"
func TestMiddlewareCoversOnlyNotes(t *testing.T) {
	_, srv := fullApp(t, nil)
	const want = `{"error":{"status":401,"code":"unauthorized","message":"a valid X-API-Key header is required"}}`
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/notes"},
		{http.MethodPost, "/notes"},
		{http.MethodGet, "/notes/1"},
		{http.MethodDelete, "/notes/1"},
	} {
		r := curl(t, srv, c.method, c.path, "", nil)
		if r.status != http.StatusUnauthorized || strings.TrimSpace(r.body) != want {
			t.Errorf("%s %s without a key: %d %q, want 401 %s", c.method, c.path, r.status, r.body, want)
		}
	}
	if r := curl(t, srv, http.MethodPost, "/keys", `{"subject":"alice","name":"laptop"}`, admin); r.status != http.StatusCreated {
		t.Errorf("POST /keys without an API key: %d %s, want 201", r.status, r.body)
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "`ctx.Principal()` returns the principal the middleware stored. The subject
// of an API key principal is the subject the key was generated for, `alice` in
// step 4."
func TestNotesAreOwnedByTheKeySubject(t *testing.T) {
	a, srv := fullApp(t, nil)
	key := issueKey(t, srv, "alice")
	if r := curl(t, srv, http.MethodPost, "/notes", `{"title":"First note","body":"Hello from Gas"}`,
		map[string]string{"X-API-Key": key}); r.status != http.StatusCreated {
		t.Fatalf("POST /notes: %d %s", r.status, r.body)
	}
	var owner string
	if err := dbOf(t, a).QueryRowContext(t.Context(), `SELECT owner FROM notes`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "alice" {
		t.Fatalf("owner %q, want alice", owner)
	}
}

// Docs: start/tutorial.mdx#_top
// "Each client authenticates with an API key and sees only its own notes."
// Docs: start/tutorial.mdx#step-5-the-notes-service
// "`get` and `delete` look a note up by id and by owner, so a note that
// belongs to another subject is a 404, the same as one that does not exist:"
// Docs: start/tutorial.mdx#step-6-test-it
// "and checks that a second subject cannot read the first one's note"
func TestAnotherSubjectsNoteIsNotFound(t *testing.T) {
	_, srv := fullApp(t, nil)
	alice := map[string]string{"X-API-Key": issueKey(t, srv, "alice")}
	bob := map[string]string{"X-API-Key": issueKey(t, srv, "bob")}
	if r := curl(t, srv, http.MethodPost, "/notes", `{"title":"First note","body":"Hello from Gas"}`, alice); r.status != http.StatusCreated {
		t.Fatalf("POST /notes: %d %s", r.status, r.body)
	}

	missing := curl(t, srv, http.MethodGet, "/notes/999", "", bob)
	if missing.status != http.StatusNotFound {
		t.Fatalf("GET /notes/999: %d, want 404", missing.status)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		r := curl(t, srv, method, "/notes/1", "", bob)
		if r.status != http.StatusNotFound || r.body != missing.body {
			t.Errorf("%s alice's note as bob: %d %q, want the 404 of a missing note %q", method, r.status, r.body, missing.body)
		}
	}
	if r := curl(t, srv, http.MethodGet, "/notes", "", bob); strings.TrimSpace(r.body) != "[]" {
		t.Errorf("bob's list: %q, want []", r.body)
	}
	if r := curl(t, srv, http.MethodGet, "/notes/1", "", alice); r.status != http.StatusOK {
		t.Errorf("alice's note after bob's delete: %d, want 200", r.status)
	}
}

// Docs: start/tutorial.mdx#step-5-the-notes-service
// "A returned error that is not a `*gas.Error`, such as the wrapped database
// errors, becomes a 500 with the message `internal server error`. The real
// error is logged, and never sent to the client."
func TestDatabaseErrorIsAnInternalError(t *testing.T) {
	sink := captureSlog(t)
	a, srv := fullApp(t, nil)
	key := issueKey(t, srv, "alice")
	if _, err := dbOf(t, a).ExecContext(t.Context(), `DROP TABLE notes`); err != nil {
		t.Fatal(err)
	}

	r := curl(t, srv, http.MethodGet, "/notes", "", map[string]string{"X-API-Key": key})
	var out struct {
		Error struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	if r.status != http.StatusInternalServerError || out.Error.Status != 500 || out.Error.Message != "internal server error" {
		t.Errorf("%d %s, want 500 with message internal server error", r.status, r.body)
	}
	if strings.Contains(r.body, "no such table") || strings.Contains(r.body, "listing notes") {
		t.Errorf("response %s carries the real error", r.body)
	}
	logged := false
	for _, rec := range sink.records(t) {
		if e, _ := rec["error"].(string); strings.Contains(e, "listing notes") && strings.Contains(e, "no such table") {
			logged = true
		}
	}
	if !logged {
		t.Error("no log record carries the real error")
	}
}

// Docs: start/tutorial.mdx#run-it-3
// "The route map now lists the notes routes:" and "... INFO route map (5
// routes across 2 services): ───────────────────────── [keys]: POST /keys
// [notes]: POST /notes/ GET /notes/ GET /notes/{id} DELETE /notes/{id}
// ─────────────────────────"
func TestRunStep5RouteMap(t *testing.T) {
	sink := captureSlog(t)
	port := freePort(t)
	project(t, withPort(t, port))
	r := runApp(t, app.New(loadConfig(t)), port)
	if err := r.stop(syscall.SIGINT); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = "route map (5 routes across 2 services):\n" +
		"─────────────────────────\n" +
		"  [keys]:\n" +
		"      POST    /keys\n" +
		"  [notes]:\n" +
		"      POST    /notes/\n" +
		"      GET     /notes/\n" +
		"      GET     /notes/{id}\n" +
		"      DELETE  /notes/{id}\n" +
		"─────────────────────────"
	if got := routeMap(t, sink); got != want {
		t.Errorf("route map:\n%s\nwant:\n%s", got, want)
	}
}

// Docs: start/tutorial.mdx#run-it-3
// "The key from step 4 still works, because it is stored in `notes.db`."
func TestKeySurvivesRestart(t *testing.T) {
	dir := project(t, pageConfig(t))
	first := app.New(loadConfig(t))
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(first.Handler())
	key := issueKey(t, srv, "alice")
	srv.Close()
	if err := first.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.db")); err != nil {
		t.Fatal(err)
	}

	second := app.New(loadConfig(t))
	start(t, second)
	if r := curl(t, serve(t, second), http.MethodGet, "/notes", "", map[string]string{"X-API-Key": key}); r.status != http.StatusOK {
		t.Fatalf("the earlier key after a restart: %d %s, want 200", r.status, r.body)
	}
}

// Docs: start/tutorial.mdx#run-it-3
// "Create two notes:" and
// "{"id":1,"title":"First note","body":"Hello from Gas","created_at":"2026-10-21T16:05:42.844152Z"}
// {"id":2,"title":"Second note","body":"SQLite underneath","created_at":"2026-10-21T16:05:42.852999Z"}"
// and "A note without a title fails validation with a 422 that names the
// field:" and
// "{ "error": { "status": 422, "code": "validation_failed", "message": "request
// validation failed", "fields": [ { "field": "title", "rule": "required",
// "message": "is required" } ] } }"
// and "List, get, delete, and get again:" and "204" and
// "{ "error": { "status": 404, "code": "not_found", "message": "note not found" } }"
// created_at varies per run, so it is matched as an RFC 3339 UTC timestamp.
// Assumption: list and get show the created_at create returned, as the
// page's output does.
func TestCurlSession(t *testing.T) {
	_, srv := fullApp(t, nil)
	key := map[string]string{"X-API-Key": issueKey(t, srv, "alice")}

	stamp := `"created_at":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z"`
	notesWant := []*regexp.Regexp{
		regexp.MustCompile(`^\{"id":1,"title":"First note","body":"Hello from Gas",` + stamp + `\}$`),
		regexp.MustCompile(`^\{"id":2,"title":"Second note","body":"SQLite underneath",` + stamp + `\}$`),
	}
	var created []string
	for i, body := range []string{
		`{"title":"First note","body":"Hello from Gas"}`,
		`{"title":"Second note","body":"SQLite underneath"}`,
	} {
		r := curl(t, srv, http.MethodPost, "/notes", body, key)
		got := strings.TrimSpace(r.body)
		if r.status != http.StatusCreated || !notesWant[i].MatchString(got) {
			t.Fatalf("POST /notes %s: %d %s, want 201 matching %s", body, r.status, got, notesWant[i])
		}
		created = append(created, got)
	}

	r := curl(t, srv, http.MethodPost, "/notes", `{"body":"no title"}`, key)
	const invalid = `{"error":{"status":422,"code":"validation_failed","message":"request validation failed",` +
		`"fields":[{"field":"title","rule":"required","message":"is required"}]}}`
	if r.status != http.StatusUnprocessableEntity || strings.TrimSpace(r.body) != invalid {
		t.Errorf("no title: %d %s, want 422 %s", r.status, r.body, invalid)
	}

	if r := curl(t, srv, http.MethodGet, "/notes", "", key); strings.TrimSpace(r.body) != "["+created[0]+","+created[1]+"]" {
		t.Errorf("GET /notes: %d %s, want [%s,%s]", r.status, r.body, created[0], created[1])
	}
	if r := curl(t, srv, http.MethodGet, "/notes/1", "", key); r.status != http.StatusOK || strings.TrimSpace(r.body) != created[0] {
		t.Errorf("GET /notes/1: %d %s, want 200 %s", r.status, r.body, created[0])
	}
	if r := curl(t, srv, http.MethodDelete, "/notes/1", "", key); r.status != http.StatusNoContent || r.body != "" {
		t.Errorf("DELETE /notes/1: %d %q, want 204 and no body", r.status, r.body)
	}
	const gone = `{"error":{"status":404,"code":"not_found","message":"note not found"}}`
	if r := curl(t, srv, http.MethodGet, "/notes/1", "", key); r.status != http.StatusNotFound || strings.TrimSpace(r.body) != gone {
		t.Errorf("GET /notes/1 after delete: %d %s, want 404 %s", r.status, r.body, gone)
	}
}

// Docs: start/tutorial.mdx#step-6-test-it
// "`Start` does what `Run` does before serving: it initializes the services,
// applies the migrations, and binds the config. It does not listen on a port."
func TestStartDoesNotListen(t *testing.T) {
	port := freePort(t)
	project(t, withPort(t, port))
	a := app.New(loadConfig(t))
	start(t, a)
	if accepting(port) {
		t.Errorf("port %d accepts connections after Start", port)
	}
	if got := a.Config().Server.Port; got != port {
		t.Errorf("bound Server.Port = %d, want %d", got, port)
	}
	if !slices.Contains(tables(t, dbOf(t, a)), "notes") {
		t.Error("Start did not apply the notes migration")
	}
	if got := a.ActiveServices(); !slices.Contains(got, "notes") || !slices.Contains(got, "keys") {
		t.Errorf("active services %v, want notes and keys initialized", got)
	}
}

// Docs: start/tutorial.mdx#step-6-test-it
// "`Handler` returns the handler the app's own server uses, which
// `httptest.NewServer` serves on a local port."
func TestHandlerIsTheServersHandler(t *testing.T) {
	a, srv := fullApp(t, nil)
	own := httptest.NewServer(a.Server().Handler)
	t.Cleanup(own.Close)
	for _, s := range []*httptest.Server{srv, own} {
		if r := curl(t, s, http.MethodGet, "/notes", "", nil); r.status != http.StatusUnauthorized {
			t.Errorf("GET /notes: %d, want 401 from the app's routes", r.status)
		}
	}
	if reflect.ValueOf(a.Server().Handler).Pointer() != reflect.ValueOf(a.Handler()).Pointer() {
		t.Error("Server().Handler is not the handler Handler returns")
	}
}

// Docs: start/tutorial.mdx#step-6-test-it
// "`Stop` closes the services when the test ends."
func TestStopClosesTheServices(t *testing.T) {
	sink := captureSlog(t)
	project(t, pageConfig(t))
	a := app.New(loadConfig(t))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	db := dbOf(t, a)
	if err := a.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(t.Context()); err == nil {
		t.Error("the database still answers after Stop")
	}
	for _, name := range []string{"notes", "keys", "gas/auth/apikey", "gas/migrate", "gas/database"} {
		if len(sink.find(t, "closing service", map[string]string{"service": name})) != 1 {
			t.Errorf("Stop did not close %s", name)
		}
	}
}

// Docs: start/tutorial.mdx#run-it-4
// "go test ./..." and "ok example.com/notesapi/app 0.368s ? example.com/notesapi/cmd
// [no test files] ? example.com/notesapi/keys [no test files] ?
// example.com/notesapi/notes [no test files]"
// The module path is the docs one here, and the timing varies.
func TestTutorialTestPasses(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./start-tutorial/...")
	cmd.Dir = snippetsDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test ./start-tutorial/...: %v\n%s", err, out)
	}
	const mod = "github.com/gasmod/gas/docs/snippets/start-tutorial"
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^ok\s+` + regexp.QuoteMeta(mod+"/app") + `\s`),
		regexp.MustCompile(`(?m)^\?\s+` + regexp.QuoteMeta(mod+"/cmd") + `\s+\[no test files\]$`),
		regexp.MustCompile(`(?m)^\?\s+` + regexp.QuoteMeta(mod+"/keys") + `\s+\[no test files\]$`),
		regexp.MustCompile(`(?m)^\?\s+` + regexp.QuoteMeta(mod+"/notes") + `\s+\[no test files\]$`),
	} {
		if !want.Match(out) {
			t.Errorf("go test output lacks a line matching %s:\n%s", want, out)
		}
	}
}
