package modulesconfig_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers/secretsmanager"
)

// fakeSecrets is a Secrets Manager endpoint serving fixed secret strings. It
// records the secret ids it was asked for and the access key of each request.
type fakeSecrets struct {
	*httptest.Server
	mu      sync.Mutex
	secrets map[string]string
	asked   []string
	keys    []string
}

func newFakeSecrets(t *testing.T, secrets map[string]string) *fakeSecrets {
	t.Helper()
	f := &fakeSecrets{secrets: secrets}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ SecretId string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, in.SecretId)
		auth := r.Header.Get("Authorization")
		if _, after, ok := strings.Cut(auth, "Credential="); ok {
			key, _, _ := strings.Cut(after, "/")
			f.keys = append(f.keys, key)
		}
		secret, ok := f.secrets[in.SecretId]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ARN": "arn:aws:secretsmanager:us-east-1:000000000000:secret:" + in.SecretId, "Name": in.SecretId,
			"SecretString": secret, "VersionId": "v1",
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSecrets) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeSecrets) provider(opts ...secretsmanager.Option) *secretsmanager.Provider {
	return secretsmanager.NewProvider(append([]secretsmanager.Option{
		secretsmanager.WithEndpoint(f.URL),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("test", "test"),
	}, opts...)...)
}

// isolateAWS points the AWS default configuration at files that do not
// exist and sets the credentials the default chain finds.
func isolateAWS(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("AWS_CONFIG_FILE", missing)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", missing)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "fromchain")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fromchain-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	_ = os.Unsetenv("AWS_PROFILE")
}

// Docs: modules/config.mdx#aws-secrets-manager
// "Secrets are fetched on every load, in registration order, and merged with later ones winning. A secret registered with `WithSecret` must hold a JSON object, which is merged at the root."
func TestSecretsFetchedEveryLoadInOrder(t *testing.T) {
	t.Parallel()
	f := newFakeSecrets(t, map[string]string{
		"first":  `{"db":{"user":"u","password":"first"},"only":"first"}`,
		"second": `{"db":{"password":"second"}}`,
	})
	cfg := config.New(config.WithProvider(f.provider(secretsmanager.WithSecret("first"), secretsmanager.WithSecret("second"))))
	if len(f.requests()) != 0 {
		t.Fatal("fetched before Load")
	}
	for range 2 {
		if err := cfg.Load(); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.requests(); strings.Join(got, ",") != "first,second,first,second" {
		t.Fatalf("requests = %v, want both secrets in order on every load", got)
	}
	for key, want := range map[string]string{"db.user": "u", "db.password": "second", "only": "first"} {
		if got := cfg.Get(key); got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
}

// Docs: modules/config.mdx#aws-secrets-manager
// "`WithSecretAtKey` places a secret at a dot-separated key: a JSON object is nested there, and any other value is stored there as a string."
func TestSecretAtKey(t *testing.T) {
	t.Parallel()
	f := newFakeSecrets(t, map[string]string{"obj": `{"user":"u"}`, "plain": "s3cret", "num": "42"})
	cfg := load(t, config.WithProvider(f.provider(
		secretsmanager.WithSecretAtKey("obj", "database.creds"),
		secretsmanager.WithSecretAtKey("plain", "database.password"),
		secretsmanager.WithSecretAtKey("num", "app.limit"),
	)))
	if cfg.Get("database.creds.user") != "u" {
		t.Errorf("object at database.creds: %v", cfg.Get("database.creds"))
	}
	if got, ok := cfg.Get("database.password").(string); !ok || got != "s3cret" {
		t.Errorf("database.password = %#v, want the string s3cret", cfg.Get("database.password"))
	}
	if got, ok := cfg.Get("app.limit").(string); !ok || got != "42" {
		t.Errorf("app.limit = %#v, want the string 42", cfg.Get("app.limit"))
	}
}

// Docs: modules/config.mdx#aws-secrets-manager
// "static credentials are used only when both values are set, and otherwise the default AWS configuration and credential chain apply."
func TestSecretsStaticCredentials(t *testing.T) {
	isolateAWS(t)
	f := newFakeSecrets(t, map[string]string{"s": `{"k":"v"}`})
	for _, c := range []struct {
		id, secret, want string
	}{
		{"statickey", "staticsecret", "statickey"},
		{"statickey", "", "fromchain"},
		{"", "staticsecret", "fromchain"},
	} {
		p := secretsmanager.NewProvider(secretsmanager.WithEndpoint(f.URL), secretsmanager.WithRegion("us-east-1"),
			secretsmanager.WithStaticCredentials(c.id, c.secret), secretsmanager.WithSecret("s"))
		if _, err := p.Load(); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		f.mu.Lock()
		got := f.keys[len(f.keys)-1]
		f.mu.Unlock()
		if got != c.want {
			t.Errorf("static (%q, %q): signed with %q, want %q", c.id, c.secret, got, c.want)
		}
	}
}

// fakeAPI is an injected client that records the context of each call.
type fakeAPI struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (a *fakeAPI) GetSecretValue(ctx context.Context, in *awssm.GetSecretValueInput, _ ...func(*awssm.Options)) (*awssm.GetSecretValueOutput, error) {
	a.mu.Lock()
	a.ctxs = append(a.ctxs, ctx)
	a.mu.Unlock()
	s := `{"from":"injected"}`
	return &awssm.GetSecretValueOutput{Name: in.SecretId, SecretString: &s}, nil
}

func (a *fakeAPI) last() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctxs[len(a.ctxs)-1]
}

// Docs: modules/config.mdx#aws-secrets-manager
// "`WithClient` injects a client instead and makes those options ignored."
func TestSecretsWithClient(t *testing.T) {
	t.Parallel()
	f := newFakeSecrets(t, map[string]string{"s": `{"from":"endpoint"}`})
	api := &fakeAPI{}
	cfg := load(t, config.WithProvider(f.provider(secretsmanager.WithClient(api), secretsmanager.WithSecret("s"))))
	if cfg.Get("from") != "injected" || len(f.requests()) != 0 {
		t.Fatalf("from=%v, endpoint requests %v; want only the injected client used", cfg.Get("from"), f.requests())
	}
}

// deadlineIn loads a provider with an injected client through load and
// returns how far off the client's context deadline was, and whether it had
// one.
func deadlineIn(t *testing.T, load func(*secretsmanager.Provider) error, opts ...secretsmanager.Option) (time.Duration, bool) {
	t.Helper()
	api := &fakeAPI{}
	begin := time.Now()
	p := secretsmanager.NewProvider(append([]secretsmanager.Option{secretsmanager.WithClient(api), secretsmanager.WithSecret("s")}, opts...)...)
	if err := load(p); err != nil {
		t.Fatal(err)
	}
	d, ok := api.last().Deadline()
	return d.Sub(begin), ok
}

// Docs: modules/config.mdx#aws-secrets-manager
// "The provider's own `Load()` bounds the whole fetch with a 10-second timeout (`secretsmanager.DefaultTimeout`), which `WithTimeout` changes; zero or a negative duration means no timeout."
func TestSecretsProviderLoadTimeout(t *testing.T) {
	t.Parallel()
	if secretsmanager.DefaultTimeout != 10*time.Second {
		t.Fatalf("DefaultTimeout = %v, want 10s", secretsmanager.DefaultTimeout)
	}
	load := func(p *secretsmanager.Provider) error {
		_, err := p.Load()
		return err
	}
	if d, ok := deadlineIn(t, load); !ok || d > 11*time.Second || d < 9*time.Second {
		t.Errorf("default: deadline in %v (set %v), want about 10s", d, ok)
	}
	if d, ok := deadlineIn(t, load, secretsmanager.WithTimeout(2*time.Second)); !ok || d > 3*time.Second || d < time.Second {
		t.Errorf("WithTimeout(2s): deadline in %v (set %v), want about 2s", d, ok)
	}
	for _, zero := range []time.Duration{0, -time.Second} {
		if _, ok := deadlineIn(t, load, secretsmanager.WithTimeout(zero)); ok {
			t.Errorf("WithTimeout(%v): a deadline was set, want none", zero)
		}
	}
}

// Docs: modules/config.mdx#aws-secrets-manager
// "`config.Config` never calls that method: `Load` and `LoadWithContext` call the provider's `LoadContext`, which applies no timeout, so through the config the context passed to `LoadWithContext` is the only bound (`Load` passes `context.Background()`, which has none)."
func TestSecretsConfigLoadNoTimeout(t *testing.T) {
	t.Parallel()
	viaLoad := func(p *secretsmanager.Provider) error { return config.New(config.WithProvider(p)).Load() }
	for _, opts := range [][]secretsmanager.Option{nil, {secretsmanager.WithTimeout(2 * time.Second)}} {
		if _, ok := deadlineIn(t, viaLoad, opts...); ok {
			t.Errorf("config Load with %d options: the client saw a deadline, want none", len(opts))
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	viaCtx := func(p *secretsmanager.Provider) error {
		return config.New(config.WithProvider(p)).LoadWithContext(ctx)
	}
	api := &fakeAPI{}
	p := secretsmanager.NewProvider(secretsmanager.WithClient(api), secretsmanager.WithSecret("s"), secretsmanager.WithTimeout(time.Second))
	if err := viaCtx(p); err != nil {
		t.Fatal(err)
	}
	if got, ok := api.last().Deadline(); !ok || !got.Equal(want) {
		t.Errorf("LoadWithContext: client deadline %v (set %v), want only the caller's %v", got, ok, want)
	}
}

// Docs: modules/config.mdx#aws-secrets-manager
// "`LoadWithContext` passes the caller's context through unchanged."
func TestSecretsLoadWithContext(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{}
	cfg := config.New(config.WithProvider(secretsmanager.NewProvider(secretsmanager.WithClient(api), secretsmanager.WithSecret("s"))))
	ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
	if err := cfg.LoadWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	got := api.last()
	want, wantOK := ctx.Deadline()
	have, haveOK := got.Deadline()
	if got.Value(ctxKey{}) != "caller" || have != want || haveOK != wantOK {
		t.Fatalf("client saw value %v, deadline %v/%v; want the caller's context (%v/%v)", got.Value(ctxKey{}), have, haveOK, want, wantOK)
	}
}

// Docs: modules/config.mdx#aws-secrets-manager
// "Registering no secret at all makes `Load` fail with `secretsmanager.ErrNoSecretsConfigured`."
func TestSecretsNoneConfigured(t *testing.T) {
	t.Parallel()
	err := config.New(config.WithProvider(secretsmanager.NewProvider(secretsmanager.WithClient(&fakeAPI{})))).Load()
	if !errors.Is(err, secretsmanager.ErrNoSecretsConfigured) || !errors.Is(err, config.ErrProviderLoadFailed) {
		t.Fatalf("Load = %v, want ErrNoSecretsConfigured wrapped in ErrProviderLoadFailed", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `secretsmanager.ErrClientInitFailed` | The AWS configuration could not be loaded. |"
func TestSecretsClientInitFailed(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_PROFILE", "docsclaim-no-such-profile")
	_, err := secretsmanager.NewProvider(secretsmanager.WithSecret("s")).Load()
	if !errors.Is(err, secretsmanager.ErrClientInitFailed) {
		t.Fatalf("Load = %v, want ErrClientInitFailed", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `secretsmanager.ErrSecretFetchFailed` | `GetSecretValue` failed. The error names the secret. |"
func TestSecretsFetchFailed(t *testing.T) {
	t.Parallel()
	f := newFakeSecrets(t, map[string]string{})
	_, err := f.provider(secretsmanager.WithSecret("docsclaim/missing")).Load()
	if !errors.Is(err, secretsmanager.ErrSecretFetchFailed) || !strings.Contains(err.Error(), "docsclaim/missing") {
		t.Fatalf("Load = %v, want ErrSecretFetchFailed naming docsclaim/missing", err)
	}
}

// Docs: modules/config.mdx#errors
// "| `secretsmanager.ErrSecretDecodeFailed` | A secret registered with `WithSecret` is not a JSON object. |"
func TestSecretsDecodeFailed(t *testing.T) {
	t.Parallel()
	f := newFakeSecrets(t, map[string]string{"plain": "s3cret", "list": `[1,2]`})
	for _, name := range []string{"plain", "list"} {
		_, err := f.provider(secretsmanager.WithSecret(name)).Load()
		if !errors.Is(err, secretsmanager.ErrSecretDecodeFailed) {
			t.Errorf("%s: Load = %v, want ErrSecretDecodeFailed", name, err)
		}
	}
}
