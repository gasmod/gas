package dir

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

// TestNewProvider runs the constructor cases of NewProvider.
func TestNewProvider(t *testing.T) {
	t.Parallel()

	t.Run("success with configured bucket", testNewProviderSuccessWithConfiguredBucket)

	t.Run("empty configured bucket is allowed", testNewProviderEmptyConfiguredBucketIsAllowed)

	t.Run("invalid configured bucket", testNewProviderInvalidConfiguredBucket)

	t.Run(
		"invalid bucket reported before missing root",
		testNewProviderInvalidBucketReportedBeforeMissingRoot,
	)

	t.Run("root required", testNewProviderRootRequired)

	t.Run("WithRoot(nil) is root required", testNewProviderWithRootNilIsRootRequired)

	t.Run("last WithRoot wins", testNewProviderLastWithRootWins)

	t.Run("missing config provider", testNewProviderMissingConfigProvider)

	t.Run("bind failure", testNewProviderBindFailure)

	t.Run("closed root", testNewProviderClosedRoot)

	t.Run("constructed provider is usable", testNewProviderConstructedProviderIsUsable)
}

// testNewProviderSuccessWithConfiguredBucket checks that NewProvider keeps
// the root and configured bucket and creates only the internal directories.
func testNewProviderSuccessWithConfiguredBucket(t *testing.T) {
	t.Parallel()
	root, dir := openRoot(t)
	p, err := NewProvider(WithRoot(root))(newConfig(t, "my-bucket"))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if p.Root() != root {
		t.Errorf("Root() = %p, want %p", p.Root(), root)
	}
	if p.config.Storage.Bucket != "my-bucket" {
		t.Errorf("config bucket = %q, want my-bucket", p.config.Storage.Bucket)
	}
	// NewProvider lays out the shared internal directories and no bucket.
	if got := listDir(t, dir); len(got) != 1 || got[0] != ".gas" {
		t.Errorf("root = %v, want only .gas", got)
	}
}

// testNewProviderEmptyConfiguredBucketIsAllowed checks that no configured
// bucket is not an error.
func testNewProviderEmptyConfiguredBucketIsAllowed(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	p, err := NewProvider(WithRoot(root))(newConfig(t, ""))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if p.config.Storage.Bucket != "" {
		t.Errorf("config bucket = %q, want empty", p.config.Storage.Bucket)
	}
}

// testNewProviderInvalidConfiguredBucket checks that an invalid configured
// bucket fails with ErrInvalidBucket.
func testNewProviderInvalidConfiguredBucket(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	for _, bk := range []string{"UPPER", "a_b", "ab", "192.168.0.1", "-abc"} {
		p, err := NewProvider(WithRoot(root))(newConfig(t, bk))
		if !errors.Is(err, storage.ErrInvalidBucket) {
			t.Errorf("NewProvider(bucket=%q) error = %v, want ErrInvalidBucket", bk, err)
		}
		if p != nil {
			t.Errorf("NewProvider(bucket=%q) returned non-nil provider", bk)
		}
		// The configured name is operator configuration, and the error names
		// it as the s3 provider does.
		if err != nil && !strings.Contains(err.Error(), bk) {
			t.Errorf("error %q should name the bucket %q", err, bk)
		}
	}
}

// testNewProviderInvalidBucketReportedBeforeMissingRoot checks that the
// bucket is validated before the root.
func testNewProviderInvalidBucketReportedBeforeMissingRoot(t *testing.T) {
	t.Parallel()
	_, err := NewProvider()(newConfig(t, "BAD"))
	if !errors.Is(err, storage.ErrInvalidBucket) {
		t.Errorf("NewProvider() error = %v, want ErrInvalidBucket", err)
	}
}

// testNewProviderRootRequired checks that a missing root fails with
// ErrRootRequired.
func testNewProviderRootRequired(t *testing.T) {
	t.Parallel()
	p, err := NewProvider()(newConfig(t, "my-bucket"))
	if !errors.Is(err, ErrRootRequired) || !errors.Is(err, storage.ErrNotInitialized) {
		t.Fatalf("NewProvider() error = %v, want ErrRootRequired wrapping ErrNotInitialized", err)
	}
	if p != nil {
		t.Error("NewProvider() returned non-nil provider")
	}
	if !strings.HasPrefix(err.Error(), "gas/storage/dir/provider: ") {
		t.Errorf("error = %q, want gas/storage/dir/provider prefix", err)
	}
}

// testNewProviderWithRootNilIsRootRequired checks that WithRoot(nil) is a
// missing root.
func testNewProviderWithRootNilIsRootRequired(t *testing.T) {
	t.Parallel()
	_, err := NewProvider(WithRoot(nil))(newConfig(t, "my-bucket"))
	if !errors.Is(err, ErrRootRequired) {
		t.Fatalf("NewProvider() error = %v, want ErrRootRequired", err)
	}
}

// testNewProviderLastWithRootWins checks that the last WithRoot option is
// used.
func testNewProviderLastWithRootWins(t *testing.T) {
	t.Parallel()
	r1, _ := openRoot(t)
	r2, _ := openRoot(t)
	p, err := NewProvider(WithRoot(r1), WithRoot(r2))(newConfig(t, ""))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if p.Root() != r2 {
		t.Error("Root() is not the last WithRoot value")
	}
}

// testNewProviderMissingConfigProvider checks that a nil gas.ConfigProvider
// fails construction instead of panicking.
func testNewProviderMissingConfigProvider(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	p, err := NewProvider(WithRoot(root))(nil)
	if err == nil {
		t.Fatal("NewProvider() error = nil, want error")
	}
	if p != nil {
		t.Error("NewProvider() returned non-nil provider")
	}
	// There is no sentinel for a nil config provider; the message says which
	// step failed.
	want := "gas/storage/dir/provider: config provider is nil"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

// testNewProviderBindFailure checks that a config bind error is returned
// wrapped.
func testNewProviderBindFailure(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	bindErr := errors.New("bind exploded")
	mock := &configtest.MockConfig{
		BindFn: func(any, ...config.BindOption) error { return bindErr },
	}
	p, err := NewProvider(WithRoot(root))(mock)
	if !errors.Is(err, bindErr) || p != nil {
		t.Fatalf("NewProvider() = %v, %v; want nil and an error wrapping %v", p, err, bindErr)
	}
}

// testNewProviderClosedRoot checks that a root that cannot hold the internal
// directories fails construction with ErrNotInitialized and the filesystem
// cause.
func testNewProviderClosedRoot(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cErr := root.Close(); cErr != nil {
		t.Fatal(cErr)
	}
	p, err := NewProvider(WithRoot(root))(newConfig(t, ""))
	if !errors.Is(err, storage.ErrNotInitialized) || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("NewProvider() error = %v, want ErrNotInitialized wrapping ErrClosed", err)
	}
	if errors.Is(err, ErrRootRequired) {
		t.Errorf("NewProvider() error = %v, must not be ErrRootRequired", err)
	}
	if p != nil {
		t.Error("NewProvider() returned non-nil provider")
	}
}

// testNewProviderConstructedProviderIsUsable checks that a constructed
// provider stores and reads objects.
func testNewProviderConstructedProviderIsUsable(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	p, err := NewProvider(WithRoot(root))(newConfig(t, "my-bucket"))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	createBucket(t, p, "my-bucket")
	put(t, p, "hello.txt", []byte("hi"))
	_, data := get(t, p, "hello.txt")
	if string(data) != "hi" {
		t.Errorf("body = %q, want hi", data)
	}
}

// TestNewProvider_DIWiring registers the constructor the way an application
// would, through gas.NewWorker, and builds it.
func TestNewProvider_DIWiring(t *testing.T) {
	t.Parallel()

	root, _ := openRoot(t)
	mock, err := configtest.NewMockConfigWithValues(map[string]any{
		"storage": map[string]any{"bucket": "my-bucket"},
	})
	if err != nil {
		t.Fatal(err)
	}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](mock),
		gas.WithSingletonService[storage.Provider](NewProvider(WithRoot(root))),
	)
	if initErr := w.InitServices(context.Background()); initErr != nil {
		t.Fatalf("InitServices() error: %v", initErr)
	}
	p, err := w.ServiceContainer().Resolve[storage.Provider]()
	if err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	ctx := context.Background()
	if _, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "my-bucket"}); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	if _, err := p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: base("k")}); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
}

// TestNewProvider_DIMissingConfigProvider checks that an application that
// registers no gas.ConfigProvider gets the container's resolution error at
// startup.
func TestNewProvider_DIMissingConfigProvider(t *testing.T) {
	t.Parallel()

	root, _ := openRoot(t)
	w := gas.NewWorker(
		gas.WithSingletonService[storage.Provider](NewProvider(WithRoot(root))),
	)
	err := w.InitServices(context.Background())
	if !errors.Is(err, gas.ErrNotRegistered) ||
		!strings.Contains(err.Error(), "no registration for gas.ConfigProvider") {
		t.Fatalf("InitServices() error = %v, want no registration for gas.ConfigProvider", err)
	}
}

// TestProvider_ZeroValue checks that a Provider that NewProvider did not
// build reports ErrNotInitialized from every method instead of panicking,
// before it looks at the input.
func TestProvider_ZeroValue(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	ctx := context.Background()

	if p.Root() != nil {
		t.Error("Root() on zero value should be nil")
	}

	_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "bucket"})
	wantErrIs(t, "CreateBucket", err, storage.ErrNotInitialized)
	_, err = p.CreateBucket(ctx, nil)
	wantErrIs(t, "CreateBucket(nil)", err, storage.ErrNotInitialized)

	for op, err := range allOps(ctx, p, inBucket("bucket")) {
		wantErrIs(t, op, err, storage.ErrNotInitialized)
	}

	_, err = p.HeadObject(ctx, nil)
	wantErrIs(t, "HeadObject(nil)", err, storage.ErrNotInitialized)
	_, err = p.PutObject(ctx, nil)
	wantErrIs(t, "PutObject(nil)", err, storage.ErrNotInitialized)
	_, err = p.GetObject(ctx, nil)
	wantErrIs(t, "GetObject(nil)", err, storage.ErrNotInitialized)
	_, err = p.DeleteObject(ctx, nil)
	wantErrIs(t, "DeleteObject(nil)", err, storage.ErrNotInitialized)
	_, err = p.ListObjects(ctx, &storage.ListObjectsInput{})
	wantErrIs(t, "ListObjects", err, storage.ErrNotInitialized)
	_, err = p.ListObjects(ctx, nil)
	wantErrIs(t, "ListObjects(nil)", err, storage.ErrNotInitialized)
	_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
	wantErrIs(t, "ListBuckets", err, storage.ErrNotInitialized)
	_, err = p.ListBuckets(ctx, nil)
	wantErrIs(t, "ListBuckets(nil)", err, storage.ErrNotInitialized)
	_, err = p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "bucket"})
	wantErrIs(t, "HeadBucket", err, storage.ErrNotInitialized)
	_, err = p.HeadBucket(ctx, nil)
	wantErrIs(t, "HeadBucket(nil)", err, storage.ErrNotInitialized)
	err = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "bucket"})
	wantErrIs(t, "DeleteBucket", err, storage.ErrNotInitialized)
	err = p.DeleteBucket(ctx, nil)
	wantErrIs(t, "DeleteBucket(nil)", err, storage.ErrNotInitialized)
}

// TestProvider_NilInput checks that every method rejects a nil input with
// ErrInputRequired and its operation prefix.
func TestProvider_NilInput(t *testing.T) {
	t.Parallel()

	p, _ := newTestProvider(t)
	ctx := context.Background()

	tests := []struct {
		call func() error
		name string
		op   string
	}{
		{name: "CreateBucket", op: "create bucket", call: func() error {
			_, err := p.CreateBucket(ctx, nil)
			return err
		}},
		{name: "HeadObject", op: "head", call: func() error {
			_, err := p.HeadObject(ctx, nil)
			return err
		}},
		{name: "PutObject", op: "put", call: func() error {
			_, err := p.PutObject(ctx, nil)
			return err
		}},
		{name: "GetObject", op: "get", call: func() error {
			_, err := p.GetObject(ctx, nil)
			return err
		}},
		{name: "DeleteObject", op: "delete", call: func() error {
			_, err := p.DeleteObject(ctx, nil)
			return err
		}},
		{name: "ListObjects", op: "list", call: func() error {
			_, err := p.ListObjects(ctx, nil)
			return err
		}},
		{name: "ListBuckets", op: "list buckets", call: func() error {
			_, err := p.ListBuckets(ctx, nil)
			return err
		}},
		{name: "HeadBucket", op: "head bucket", call: func() error {
			_, err := p.HeadBucket(ctx, nil)
			return err
		}},
		{name: "DeleteBucket", op: "delete bucket", call: func() error {
			return p.DeleteBucket(ctx, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call()
			wantErrIs(t, tt.name, err, storage.ErrInputRequired)
			want := "gas/storage/dir/provider: " + tt.op + ": "
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q, want prefix %q", err, want)
			}
		})
	}
}

// TestNewProvider_NilOption checks that a nil Option is reported as
// storage.ErrInvalidOption naming its position, instead of panicking, and
// that the root is left untouched.
func TestNewProvider_NilOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		opts func(root *os.Root) []Option
		name string
		want string
	}{
		{
			name: "only option",
			opts: func(*os.Root) []Option { return []Option{nil} },
			want: "gas/storage/dir/provider: storage: invalid option: option 0 is nil",
		},
		{
			name: "after WithRoot",
			opts: func(r *os.Root) []Option { return []Option{WithRoot(r), nil} },
			want: "gas/storage/dir/provider: storage: invalid option: option 1 is nil",
		},
		{
			name: "before WithRoot",
			opts: func(r *os.Root) []Option { return []Option{nil, WithRoot(r)} },
			want: "gas/storage/dir/provider: storage: invalid option: option 0 is nil",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, dir := openRoot(t)
			p, err := NewProvider(tt.opts(root)...)(newConfig(t, ""))
			if p != nil {
				t.Errorf("NewProvider() provider = %v, want nil", p)
			}
			if !errors.Is(err, storage.ErrInvalidOption) {
				t.Fatalf("NewProvider() error = %v, want ErrInvalidOption", err)
			}
			if errors.Is(err, storage.ErrNotInitialized) || errors.Is(err, ErrRootRequired) {
				t.Errorf("NewProvider() error = %v, must match only ErrInvalidOption", err)
			}
			if err.Error() != tt.want {
				t.Errorf("NewProvider() error = %q, want %q", err, tt.want)
			}
			if got := listDir(t, dir); len(got) != 0 {
				t.Errorf("root = %v, want untouched", got)
			}
		})
	}
}
