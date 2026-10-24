package modulesstorage_test

import (
	"context"
	"errors"
	"go/build"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// repoRoot is the repository root, relative to this package's directory.
const repoRoot = "../../../.."

// Docs: modules/storage.mdx#what-it-is-for
// "The `storage` package defines two interfaces and holds no implementation"
// Checked on the package's exported declarations: its only interface types
// are Provider and Presigner, and it declares no exported type with methods.
func TestStoragePackageHoldsInterfacesOnly(t *testing.T) {
	t.Parallel()
	pkg, err := build.ImportDir(filepath.Join(repoRoot, "storage"), 0)
	if err != nil {
		t.Fatalf("import storage package: %v", err)
	}
	src := ""
	for _, f := range pkg.GoFiles {
		b, err := os.ReadFile(filepath.Join(pkg.Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		src += string(b)
	}
	var ifaces []string
	for line := range strings.SplitSeq(src, "\n") {
		if name, ok := strings.CutPrefix(line, "type "); ok && strings.HasSuffix(line, " interface {") {
			ifaces = append(ifaces, strings.Fields(name)[0])
		}
		if strings.HasPrefix(line, "func (") {
			t.Errorf("storage package declares a method, want no implementation: %s", line)
		}
	}
	slices.Sort(ifaces)
	if !slices.Equal(ifaces, []string{"Presigner", "Provider"}) {
		t.Fatalf("storage interfaces = %v, want [Presigner Provider]", ifaces)
	}
}

// Docs: modules/storage.mdx#what-it-is-for
// "The interfaces live in this module, not in the gas core, because no other
// module uses them."
// Assumption: "other module" means the gas core and the other library
// modules, not example apps, which are consumers. Checked as: no Go file
// outside the storage module, the docs and the example apps imports
// gas/storage.
func TestNoOtherModuleImportsStorage(t *testing.T) {
	t.Parallel()
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if d.IsDir() {
			switch rel {
			case "storage", "docs", ".git", "node_modules", "example", filepath.Join("config", "examples"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), `"github.com/gasmod/gas/storage`) {
			t.Errorf("%s imports gas/storage", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Docs: modules/storage.mdx#what-it-is-for
// "`storage/s3` | `Provider`, `Presigner` | AWS S3 and S3-compatible services
// such as MinIO and LocalStack, through the AWS SDK for Go v2" and
// "`storage/dir` | `Provider` | a directory on local disk"
// LocalStack is exercised by every s3 test that uses the container; MinIO is
// not run here.
func TestBackendsImplementDocumentedInterfaces(t *testing.T) {
	t.Parallel()
	var _ storage.Provider = (*s3svc.Provider)(nil)
	var _ storage.Presigner = (*s3svc.Presigner)(nil)
	var _ storage.Provider = (*storagedir.Provider)(nil)
	if _, ok := any((*storagedir.Provider)(nil)).(storage.Presigner); ok {
		t.Fatalf("*dir.Provider implements storage.Presigner, want a Provider only")
	}
	// The raw client is the AWS SDK for Go v2 client.
	sdkV2Client((*s3svc.Provider)(nil).Client)
}

func sdkV2Client(func() *awss3.Client) {}

// Docs: modules/storage.mdx#wiring
// "`s3.NewProvider(opts...)` and `s3.NewPresigner(opts...)` return constructors of
// the shape `func(gas.ConfigProvider) (*T, error)`."
func TestS3ConstructorShape(t *testing.T) {
	t.Parallel()
	diShape[s3svc.Provider](s3svc.NewProvider())
	diShape[s3svc.Presigner](s3svc.NewPresigner())
}

// Docs: modules/storage.mdx#wiring
// "`dir.NewProvider(opts...)` has the same constructor shape"
func TestDirConstructorShape(t *testing.T) {
	t.Parallel()
	diShape[storagedir.Provider](storagedir.NewProvider())
}

func diShape[T any](func(gas.ConfigProvider) (*T, error)) {}

// consumer is a service that injects the storage interfaces.
type consumer struct {
	p storage.Provider
}

func newConsumer(p storage.Provider) (*consumer, error) { return &consumer{p: p}, nil }

type presignConsumer struct {
	p storage.Presigner
}

func newPresignConsumer(p storage.Presigner) (*presignConsumer, error) {
	return &presignConsumer{p: p}, nil
}

func startApp(t *testing.T, opts ...gas.Option) (*gas.App, error) {
	t.Helper()
	app := gas.NewApp(opts...)
	err := app.Start(context.Background())
	if err == nil {
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
	}
	return app, err
}

// Docs: modules/storage.mdx#wiring
// "The container calls them with the registered `gas.ConfigProvider`, so one
// must be registered, and each binds its [configuration](#configuration) from
// it."
func TestS3ConstructorsResolveConfigProvider(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	t.Run("registered", func(t *testing.T) {
		t.Parallel()
		app, err := startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, f.values("us-east-1", "bound-bucket"))),
			gas.WithSingletonService[storage.Provider](s3svc.NewProvider()),
			gas.WithSingletonService[storage.Presigner](s3svc.NewPresigner()),
			gas.WithSingletonService[*consumer](newConsumer),
			gas.WithSingletonService[*presignConsumer](newPresignConsumer),
		)
		if err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
		pc, err := app.ServiceContainer().Resolve[*presignConsumer]()
		if err != nil {
			t.Fatalf("resolve presignConsumer: %v", err)
		}
		up, err := pc.p.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
			PutObjectInput: storage.PutObjectInput{BaseObjectInput: obj("k")},
		})
		if err != nil {
			t.Fatalf("PresignUploadURL() error: %v", err)
		}
		// The bucket and endpoint come from the bound configuration.
		u, _ := url.Parse(up.URL)
		if u.Path != "/bound-bucket/k" || !strings.HasPrefix(up.URL, f.URL) {
			t.Fatalf("URL = %s, want %s/bound-bucket/k from the bound configuration", up.URL, f.URL)
		}
	})
	t.Run("not registered", func(t *testing.T) {
		t.Parallel()
		for name, opt := range map[string]gas.Option{
			"provider":  gas.WithSingletonService[storage.Provider](s3svc.NewProvider()),
			"presigner": gas.WithSingletonService[storage.Presigner](s3svc.NewPresigner()),
		} {
			var dep gas.Option = gas.WithSingletonService[*consumer](newConsumer)
			if name == "presigner" {
				dep = gas.WithSingletonService[*presignConsumer](newPresignConsumer)
			}
			if _, err := startApp(t, opt, dep); err == nil {
				t.Errorf("%s: Start() without a gas.ConfigProvider error = nil, want startup to fail", name)
			}
		}
	})
}

// Docs: modules/storage.mdx#constructors-and-options
// "A constructor fails, and so does startup, when: ... the configured bucket is
// set but is not a valid S3 bucket name (`storage.ErrInvalidBucket`) ... an
// option is nil (`storage.ErrInvalidOption`, with the option's zero-based
// position in the message, as in `gas/storage/s3/provider: storage: invalid
// option: option 1 is nil`) ... binding fails."
func TestConstructorFailures(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	good := f.values("us-east-1", "")
	bad := f.values("us-east-1", "Not_A_Valid_Bucket")

	t.Run("invalid bucket", func(t *testing.T) {
		t.Parallel()
		_, err := s3svc.NewProvider()(mockConfig(t, bad))
		wantErr(t, "s3.NewProvider", err, storage.ErrInvalidBucket)
		_, err = s3svc.NewPresigner()(mockConfig(t, bad))
		wantErr(t, "s3.NewPresigner", err, storage.ErrInvalidBucket)
		_, err = startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, bad)),
			gas.WithSingletonService[storage.Provider](s3svc.NewProvider()),
			gas.WithSingletonService[*consumer](newConsumer),
		)
		wantErr(t, "Start", err, storage.ErrInvalidBucket)
	})

	t.Run("nil option", func(t *testing.T) {
		t.Parallel()
		_, err := s3svc.NewProvider(s3svc.WithClient(nil), nil)(mockConfig(t, good))
		wantErr(t, "s3.NewProvider(_, nil)", err, storage.ErrInvalidOption)
		if err != nil && err.Error() != "gas/storage/s3/provider: storage: invalid option: option 1 is nil" {
			t.Errorf("s3.NewProvider(_, nil) message = %q, want the documented one", err.Error())
		}
		_, err = s3svc.NewPresigner(nil)(mockConfig(t, good))
		wantErr(t, "s3.NewPresigner(nil)", err, storage.ErrInvalidOption)
		if err != nil && !strings.Contains(err.Error(), "option 0 is nil") {
			t.Errorf("s3.NewPresigner(nil) message = %q, want the zero-based position 0", err.Error())
		}
		_, err = startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, good)),
			gas.WithSingletonService[storage.Presigner](s3svc.NewPresigner(nil)),
			gas.WithSingletonService[*presignConsumer](newPresignConsumer),
		)
		wantErr(t, "Start", err, storage.ErrInvalidOption)
	})

	t.Run("binding fails", func(t *testing.T) {
		t.Parallel()
		bindErr := errors.New("bind failed")
		cfg := &configtest.MockConfig{BindFn: func(any, ...config.BindOption) error { return bindErr }}
		_, err := s3svc.NewProvider()(cfg)
		wantErr(t, "s3.NewProvider", err, bindErr)
		_, err = s3svc.NewPresigner()(cfg)
		wantErr(t, "s3.NewPresigner", err, bindErr)
		_, err = startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](cfg),
			gas.WithSingletonService[storage.Provider](s3svc.NewProvider()),
			gas.WithSingletonService[*consumer](newConsumer),
		)
		wantErr(t, "Start", err, bindErr)
	})
}

// Docs: modules/storage.mdx#wiring
// "Constructors send no request to S3."
func TestConstructorsSendNoRequest(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	f.provider(t, "us-east-1", "bkt")
	f.presigner(t, "us-east-1", "bkt")
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("construction sent %d requests, want 0", n)
	}
}

// Docs: modules/storage.mdx#wiring
// "Register each under the interface your services inject. The container
// matches by exact type, so registering `*s3.Provider` leaves a
// `storage.Provider` dependency unresolved and startup fails."
func TestWiringConcreteTypeFailsStartup(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	_, err := startApp(t,
		gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, f.values("us-east-1", "b-wiring"))),
		gas.WithSingletonService[*s3svc.Provider](s3svc.NewProvider()),
		gas.WithSingletonService[*consumer](newConsumer),
	)
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start() error = %v, want startup to fail with gas.ErrNotRegistered", err)
	}
}

// Docs: modules/storage.mdx#constructors-and-options
// "`WithClient` | `NewProvider` | an `*s3.Client`" and "`WithUploaderClient` |
// `NewProvider` | a transfer manager client"
// Docs: modules/storage.mdx#constructors-and-options
// "Configuration is still bound, and the configured bucket still applies, when
// a client is injected."
func TestProviderClientOptions(t *testing.T) {
	t.Parallel()
	cfgSrv := newFakeS3(t, nil)   // the endpoint in configuration
	injected := newFakeS3(t, nil) // the endpoint of the injected clients

	t.Run("WithClient", func(t *testing.T) {
		t.Parallel()
		p, err := s3svc.NewProvider(s3svc.WithClient(injected.rawClient()))(
			mockConfig(t, cfgSrv.values("us-east-1", "cfg-bucket")))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		before := len(injected.paths())
		_, _ = p.HeadObject(t.Context(), headIn("k"))
		got := injected.paths()[before:]
		if len(got) == 0 || !strings.HasPrefix(got[0], "/cfg-bucket/k") {
			t.Fatalf("injected client saw %v, want a request for /cfg-bucket/k (configured bucket applies)", got)
		}
		if n := cfgSrv.requests.Load(); n != 0 {
			t.Fatalf("configured endpoint got %d requests, want 0 with WithClient", n)
		}
		// Configuration is still bound: an invalid configured bucket still fails.
		_, err = s3svc.NewProvider(s3svc.WithClient(injected.rawClient()))(
			mockConfig(t, cfgSrv.values("us-east-1", "Bad_Bucket")))
		wantErr(t, "NewProvider(WithClient) with invalid storage.bucket", err, storage.ErrInvalidBucket)
	})

	t.Run("WithUploaderClient", func(t *testing.T) {
		t.Parallel()
		up := newFakeS3(t, nil)
		p, err := s3svc.NewProvider(s3svc.WithUploaderClient(transfermanager.New(up.rawClient())))(
			mockConfig(t, cfgSrv.values("us-east-1", "cfg-bucket")))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		_, _ = p.PutObject(t.Context(), &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: obj("up")})
		paths := up.paths()
		if len(paths) == 0 || !strings.HasPrefix(paths[0], "/cfg-bucket/up") {
			t.Fatalf("uploader client saw %v, want the PutObject upload of /cfg-bucket/up", paths)
		}
	})
}

// Docs: modules/storage.mdx#constructors-and-options
// "`WithPresignerS3Client` | `NewPresigner` | an `*s3.Client`" and
// "`WithPresignClient` | `NewPresigner` | an `*s3.PresignClient`; takes
// precedence over `WithPresignerS3Client`"
func TestPresignerClientOptions(t *testing.T) {
	t.Parallel()
	cfgSrv := newFakeS3(t, nil)
	s3c := newFakeS3(t, nil)
	pc := newFakeS3(t, nil)
	cfg := func() gas.ConfigProvider { return mockConfig(t, cfgSrv.values("us-east-1", "cfg-bucket")) }
	host := func(p *s3svc.Presigner) string {
		up, _ := presignBoth(t, p, nil)
		u, _ := url.Parse(up.URL)
		return "http://" + u.Host
	}
	for name, tc := range map[string]struct {
		opts []s3svc.PresignerOption
		want string
	}{
		"WithPresignerS3Client": {[]s3svc.PresignerOption{s3svc.WithPresignerS3Client(s3c.rawClient())}, s3c.URL},
		"WithPresignClient":     {[]s3svc.PresignerOption{s3svc.WithPresignClient(awss3.NewPresignClient(pc.rawClient()))}, pc.URL},
		"both, PresignClient first": {[]s3svc.PresignerOption{
			s3svc.WithPresignClient(awss3.NewPresignClient(pc.rawClient())), s3svc.WithPresignerS3Client(s3c.rawClient()),
		}, pc.URL},
		"both, S3Client first": {[]s3svc.PresignerOption{
			s3svc.WithPresignerS3Client(s3c.rawClient()), s3svc.WithPresignClient(awss3.NewPresignClient(pc.rawClient())),
		}, pc.URL},
	} {
		p, err := s3svc.NewPresigner(tc.opts...)(cfg())
		if err != nil {
			t.Fatalf("%s: NewPresigner() error: %v", name, err)
		}
		if got := host(p); got != tc.want {
			t.Errorf("%s: signed for %s, want %s", name, got, tc.want)
		}
	}
}

// Docs: modules/storage.mdx#constructors-and-options
// "When an option appears more than once, the last one wins. A nil client in
// that last position means the constructor builds that client from
// configuration, so `WithClient(c), WithClient(nil)` discards `c`."
func TestLastClientOptionWins(t *testing.T) {
	t.Parallel()
	cfgSrv := newFakeS3(t, nil)
	injected := newFakeS3(t, nil)
	cfg := func() gas.ConfigProvider { return mockConfig(t, cfgSrv.values("us-east-1", "cfg-bucket")) }
	hits := func(p *s3svc.Provider) (cfgHits, injHits int64) {
		c0, i0 := cfgSrv.requests.Load(), injected.requests.Load()
		_, _ = p.HeadObject(t.Context(), headIn("k"))
		return cfgSrv.requests.Load() - c0, injected.requests.Load() - i0
	}
	for name, tc := range map[string]struct {
		opts    []s3svc.ProviderOption
		wantInj bool
	}{
		"nil alone":           {[]s3svc.ProviderOption{s3svc.WithClient(nil)}, false},
		"nil then client":     {[]s3svc.ProviderOption{s3svc.WithClient(nil), s3svc.WithClient(injected.rawClient())}, true},
		"client then nil":     {[]s3svc.ProviderOption{s3svc.WithClient(injected.rawClient()), s3svc.WithClient(nil)}, false},
		"nil uploader alone":  {[]s3svc.ProviderOption{s3svc.WithUploaderClient(nil)}, false},
		"client, nil upload ": {[]s3svc.ProviderOption{s3svc.WithClient(injected.rawClient()), s3svc.WithUploaderClient(nil)}, true},
	} {
		p, err := s3svc.NewProvider(tc.opts...)(cfg())
		if err != nil {
			t.Errorf("%s: NewProvider() error = %v, want nil", name, err)
			continue
		}
		c, i := hits(p)
		if tc.wantInj && (i == 0 || c != 0) {
			t.Errorf("%s: configured endpoint %d, injected %d requests, want the injected client", name, c, i)
		}
		if !tc.wantInj && (c == 0 || i != 0) {
			t.Errorf("%s: configured endpoint %d, injected %d requests, want a client built from configuration", name, c, i)
		}
	}

	host := func(p *s3svc.Presigner) string {
		up, _ := presignBoth(t, p, nil)
		u, _ := url.Parse(up.URL)
		return "http://" + u.Host
	}
	for name, tc := range map[string]struct {
		opts []s3svc.PresignerOption
		want string
	}{
		"nil s3 client":           {[]s3svc.PresignerOption{s3svc.WithPresignerS3Client(nil)}, cfgSrv.URL},
		"nil presign client":      {[]s3svc.PresignerOption{s3svc.WithPresignClient(nil)}, cfgSrv.URL},
		"nil then s3 client":      {[]s3svc.PresignerOption{s3svc.WithPresignerS3Client(nil), s3svc.WithPresignerS3Client(injected.rawClient())}, injected.URL},
		"s3 client then nil":      {[]s3svc.PresignerOption{s3svc.WithPresignerS3Client(injected.rawClient()), s3svc.WithPresignerS3Client(nil)}, cfgSrv.URL},
		"presign client then nil": {[]s3svc.PresignerOption{s3svc.WithPresignClient(awss3.NewPresignClient(injected.rawClient())), s3svc.WithPresignClient(nil)}, cfgSrv.URL},
		"nil presign, s3 client":  {[]s3svc.PresignerOption{s3svc.WithPresignClient(nil), s3svc.WithPresignerS3Client(injected.rawClient())}, injected.URL},
	} {
		p, err := s3svc.NewPresigner(tc.opts...)(cfg())
		if err != nil {
			t.Errorf("%s: NewPresigner() error = %v, want nil", name, err)
			continue
		}
		if got := host(p); got != tc.want {
			t.Errorf("%s: signed for %s, want %s", name, got, tc.want)
		}
	}
}

func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("OpenRoot() error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, path
}

// Docs: modules/storage.mdx#wiring
// "binds `storage.bucket` from the `gas.ConfigProvider`, so one must be
// registered even when nothing is configured."
func TestDirWiring(t *testing.T) {
	t.Parallel()
	t.Run("binds storage.bucket", func(t *testing.T) {
		t.Parallel()
		root, _ := openRoot(t)
		bucket := uniqueName("wiring")
		app, err := startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, map[string]any{"bucket": bucket})),
			gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
			gas.WithSingletonService[*consumer](newConsumer),
		)
		if err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
		c, err := app.ServiceContainer().Resolve[*consumer]()
		if err != nil {
			t.Fatalf("resolve consumer: %v", err)
		}
		mustCreateBucket(t, c.p, bucket)
		mustPut(t, c.p, "k", "v")
		if got := readObject(t, c.p, "k"); got != "v" {
			t.Fatalf("GetObject(k) with nil Bucket = %q, want %q", got, "v")
		}
	})
	t.Run("nothing configured", func(t *testing.T) {
		t.Parallel()
		root, _ := openRoot(t)
		if _, err := startApp(t,
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, map[string]any{})),
			gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
			gas.WithSingletonService[*consumer](newConsumer),
		); err != nil {
			t.Fatalf("Start() with an empty gas.ConfigProvider error = %v, want nil", err)
		}
	})
	t.Run("no ConfigProvider", func(t *testing.T) {
		t.Parallel()
		root, _ := openRoot(t)
		if _, err := startApp(t,
			gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
			gas.WithSingletonService[*consumer](newConsumer),
		); err == nil {
			t.Fatalf("Start() without a gas.ConfigProvider error = nil, want startup to fail")
		}
	})
}

// Docs: modules/storage.mdx#constructors-and-options
// "`WithRoot(root)` is required; the last one given wins. Without it, or with
// `WithRoot(nil)`, the constructor returns `dir.ErrRootRequired`, which wraps
// `storage.ErrNotInitialized`."
func TestDirWithRoot(t *testing.T) {
	t.Parallel()
	cfg := mockConfig(t, map[string]any{})
	r1, p1 := openRoot(t)
	r2, p2 := openRoot(t)

	for name, opts := range map[string][]storagedir.Option{
		"no WithRoot":    nil,
		"WithRoot(nil)":  {storagedir.WithRoot(nil)},
		"root, then nil": {storagedir.WithRoot(r1), storagedir.WithRoot(nil)},
		"only skip hook": {storagedir.WithSkipHandler(func(string, error) {})},
	} {
		_, err := storagedir.NewProvider(opts...)(cfg)
		wantErr(t, name, err, storagedir.ErrRootRequired)
		wantErr(t, name, err, storage.ErrNotInitialized)
	}
	if !errors.Is(storagedir.ErrRootRequired, storage.ErrNotInitialized) {
		t.Fatalf("dir.ErrRootRequired does not wrap storage.ErrNotInitialized")
	}

	p, err := storagedir.NewProvider(storagedir.WithRoot(r1), storagedir.WithRoot(r2))(cfg)
	if err != nil {
		t.Fatalf("NewProvider(WithRoot(r1), WithRoot(r2)) error: %v", err)
	}
	mustCreateBucket(t, p, "last-wins")
	if _, err := os.Stat(filepath.Join(p2, sha("last-wins"))); err != nil {
		t.Fatalf("bucket not stored under the last root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p1, sha("last-wins"))); err == nil {
		t.Fatalf("bucket stored under the first root, want the last WithRoot to win")
	}
}

// Docs: modules/storage.mdx#wiring
// "The provider never closes the root; whoever opened it closes it once the
// provider is no longer used."
func TestDirNeverClosesRoot(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	p, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{"bucket": "b-root"}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	mustCreateBucket(t, p, "b-root")
	mustPut(t, p, "k", "v")
	_ = readObject(t, p, "k")
	if _, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{}); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: "nope-bucket"}); !errors.Is(err, storage.ErrBucketNotFound) {
		t.Fatal(err)
	}
	if _, err := root.Stat("."); err != nil {
		t.Fatalf("root after provider use: Stat error = %v, want the root still open", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("root.Close() error = %v, want the caller's Close to be the first", err)
	}
}

// Docs: modules/storage.mdx#constructors-and-options
// "The constructor creates the backend's internal directories in the root, and no
// bucket. A nil option fails with `storage.ErrInvalidOption`, and then nothing is
// created in the root."
func TestDirConstructorCreates(t *testing.T) {
	t.Parallel()
	t.Run("internal directories, no bucket", func(t *testing.T) {
		t.Parallel()
		root, path := openRoot(t)
		p, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{"bucket": "configured"}))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			t.Fatalf("root is empty after NewProvider, want the internal directories")
		}
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("root holds file %s after NewProvider, want only directories", e.Name())
			}
		}
		out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
		if err != nil {
			t.Fatalf("ListBuckets() error: %v", err)
		}
		if len(out.Buckets) != 0 {
			t.Fatalf("buckets after NewProvider = %v, want none", namesOf(out.Buckets))
		}
		_, err = p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "configured"})
		wantErr(t, "HeadBucket(configured bucket)", err, storage.ErrBucketNotFound)
	})
	t.Run("nil option", func(t *testing.T) {
		t.Parallel()
		root, path := openRoot(t)
		_, err := storagedir.NewProvider(storagedir.WithRoot(root), nil)(mockConfig(t, map[string]any{}))
		wantErr(t, "NewProvider(WithRoot, nil)", err, storage.ErrInvalidOption)
		entries, _ := os.ReadDir(path)
		if len(entries) != 0 {
			t.Fatalf("root holds %d entries after a nil option, want nothing created", len(entries))
		}
	})
}

// waitFor polls cond for up to d.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// Docs: modules/storage.mdx#constructors-and-options
// "The options' doc comments say a nil client is ignored, which is not what the
// code does"
func TestOptionDocCommentsSayNilIgnored(t *testing.T) {
	t.Parallel()
	for file, phrases := range map[string][]string{
		"provider.go":  {"A nil client is ignored.", "A nil uploader is ignored."},
		"presigner.go": {"A nil client is ignored.", "A nil presigner is ignored."},
	} {
		b, err := os.ReadFile(filepath.Join(repoRoot, "storage", "s3", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, ph := range phrases {
			if !strings.Contains(string(b), ph) {
				t.Errorf("storage/s3/%s doc comments do not say %q", file, ph)
			}
		}
	}
}
