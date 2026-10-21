package guidesfiles_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
	"github.com/gasmod/gas/storage/storagetest"
)

// Docs: guides/files.mdx#_top
// "It implements them on AWS S3 and S3-compatible services (`storage/s3`) and
// on a local directory (`storage/dir`, a `Provider` only)."
func TestBackendsImplementDocumentedInterfaces(t *testing.T) {
	t.Parallel()
	var _ storage.Provider = (*s3svc.Provider)(nil)
	var _ storage.Presigner = (*s3svc.Presigner)(nil)
	var _ storage.Provider = (*storagedir.Provider)(nil)
	if _, ok := any((*storagedir.Provider)(nil)).(storage.Presigner); ok {
		t.Fatalf("*dir.Provider implements storage.Presigner, want a Provider only")
	}
}

// Docs: guides/files.mdx#wiring
// "`s3.NewProvider()` and `s3.NewPresigner()` return DI constructors of the
// shape `func(gas.ConfigProvider) (*T, error)`"
func TestS3ConstructorShape(t *testing.T) {
	t.Parallel()
	// Compiles only if each constructor has exactly the documented shape.
	diShape[s3svc.Provider](s3svc.NewProvider())
	diShape[s3svc.Presigner](s3svc.NewPresigner())
}

func diShape[T any](func(gas.ConfigProvider) (*T, error)) {}

// consumer is a service that injects the storage interfaces, as the page's
// Service does.
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

func startApp(t *testing.T, opts ...gas.Option) error {
	t.Helper()
	app := gas.NewApp(opts...)
	err := app.Start(context.Background())
	if err == nil {
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
	}
	return err
}

// Docs: guides/files.mdx#wiring
// "register a `gas.ConfigProvider` alongside them. Register each under the
// interface your services inject."
func TestWiringUnderInterfacesStarts(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	err := startApp(t,
		gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, f.values("us-east-1", "b-wiring"))),
		gas.WithSingletonService[storage.Provider](s3svc.NewProvider()),
		gas.WithSingletonService[storage.Presigner](s3svc.NewPresigner()),
		gas.WithSingletonService[*consumer](newConsumer),
		gas.WithSingletonService[*presignConsumer](newPresignConsumer),
	)
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}
}

// Docs: guides/files.mdx#wiring
// "The container matches by exact type. Registering the provider as
// `*s3.Provider` leaves your services' `storage.Provider` dependency
// unresolved and startup fails."
func TestWiringConcreteTypeFailsStartup(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	err := startApp(t,
		gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, f.values("us-east-1", "b-wiring"))),
		gas.WithSingletonService[*s3svc.Provider](s3svc.NewProvider()),
		gas.WithSingletonService[*consumer](newConsumer),
	)
	if err == nil {
		t.Fatalf("Start() error = nil, want startup to fail")
	}
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start() error = %v, want errors.Is gas.ErrNotRegistered (unresolved dependency)", err)
	}
}

// Docs: guides/files.mdx#wiring
// "The directory backend needs an `os.Root`, which you open and close"
func TestDirBackendNeedsRoot(t *testing.T) {
	t.Parallel()
	_, err := storagedir.NewProvider()(mockConfig(t, map[string]any{}))
	if err == nil {
		t.Fatalf("dir.NewProvider() without WithRoot error = nil, want an error")
	}
}

// Docs: guides/files.mdx#wiring
// "The directory backend needs an `os.Root`, which you open and close. Its
// constructor also binds settings from a `gas.ConfigProvider`, so `Start`
// fails without one:"
// followed by the storage snippet's 'dir' region, which registers a
// gas.ConfigProvider and the dir provider under storage.Provider.
func TestDirWiringRegion(t *testing.T) {
	t.Parallel()
	openRoot := func(t *testing.T) *os.Root {
		t.Helper()
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatalf("OpenRoot() error: %v", err)
		}
		t.Cleanup(func() { _ = root.Close() })
		return root
	}

	t.Run("as shown, binding settings", func(t *testing.T) {
		t.Parallel()
		root := openRoot(t)
		bucket := uniqueName("wiring")
		app := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](mockConfig(t, map[string]any{"bucket": bucket})),
			gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
			gas.WithSingletonService[*consumer](newConsumer),
		)
		if err := app.Start(context.Background()); err != nil {
			t.Fatalf("Start() error = %v, want nil", err)
		}
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		c, err := app.ServiceContainer().Resolve[*consumer]()
		if err != nil {
			t.Fatalf("resolve consumer: %v", err)
		}
		mustCreateBucket(t, c.p, bucket)
		// storage.bucket bound from the ConfigProvider is the default for a
		// nil Bucket.
		mustPut(t, c.p, "k", "v")
		if got := readObject(t, c.p, "k"); got != "v" {
			t.Fatalf("GetObject(k) with nil Bucket = %q, want %q", got, "v")
		}
	})

	t.Run("without a ConfigProvider", func(t *testing.T) {
		t.Parallel()
		root := openRoot(t)
		err := startApp(t,
			gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
			gas.WithSingletonService[*consumer](newConsumer),
		)
		if err == nil {
			t.Fatalf("Start() without a gas.ConfigProvider error = nil, want startup to fail")
		}
	})
}

// Docs: guides/files.mdx#consuming-it
// "it can be tested against `storagetest.MockProvider` and
// `storagetest.MockPresigner` with no network."
func TestConsumerAgainstMocks(t *testing.T) {
	t.Parallel()
	mp := &storagetest.MockProvider{
		GetObjectFn: func(context.Context, *storage.GetObjectInput) (*storage.Object, error) {
			return nil, storage.ErrKeyNotFound
		},
	}
	ms := &storagetest.MockPresigner{}
	var p storage.Provider = mp
	var s storage.Presigner = ms
	if _, err := p.GetObject(t.Context(), &storage.GetObjectInput{}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("mock GetObject error = %v, want ErrKeyNotFound", err)
	}
	if _, err := s.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{}); err != nil {
		t.Fatalf("mock PresignUploadURL error = %v, want nil", err)
	}
	if got := mp.CallCount("GetObject"); got != 1 {
		t.Fatalf("MockProvider.CallCount(GetObject) = %d, want 1", got)
	}
	if got := ms.CallCount("PresignUploadURL"); got != 1 {
		t.Fatalf("MockPresigner.CallCount(PresignUploadURL) = %d, want 1", got)
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "The `storagetest` package provides `MockProvider` and `MockPresigner`,
// recording mocks of the two interfaces."
func TestStoragetestMocksRecord(t *testing.T) {
	t.Parallel()
	var p storage.Provider = &storagetest.MockProvider{}
	var s storage.Presigner = &storagetest.MockPresigner{}
	in := &storage.HeadBucketInput{Bucket: "b"}
	_, _ = p.HeadBucket(t.Context(), in)
	_, _ = s.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{})
	mp := p.(*storagetest.MockProvider)
	if len(mp.Calls) != 1 || mp.Calls[0].Method != "HeadBucket" {
		t.Fatalf("MockProvider.Calls = %+v, want one HeadBucket call", mp.Calls)
	}
	if len(mp.Calls[0].Args) == 0 || mp.Calls[0].Args[0] != any(in) {
		t.Fatalf("MockProvider.Calls[0].Args = %v, want the input", mp.Calls[0].Args)
	}
	if got := s.(*storagetest.MockPresigner).CallCount("PresignDownloadURL"); got != 1 {
		t.Fatalf("MockPresigner.CallCount(PresignDownloadURL) = %d, want 1", got)
	}
}
