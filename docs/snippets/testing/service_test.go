// Package testing shows how Gas services are tested.
package testing

// #region imports
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/storagetest"
)

// #endregion imports

// The service under test depends on the interface, not on a backend.
type Service struct {
	storage storage.Provider
	router  *gas.Router
}

func New(provider storage.Provider, router *gas.Router) *Service {
	return &Service{storage: provider, router: router}
}

func (s *Service) Name() string                { return "files" }
func (s *Service) Close(context.Context) error { return nil }

func (s *Service) Init(context.Context) error {
	s.router.Handle(s, http.MethodGet, "/files/{key}", s.download)
	return nil
}

func (s *Service) download(ctx gas.Context) error {
	obj, err := s.storage.GetObject(ctx, &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput: storage.BaseObjectInput{Key: ctx.Param("key")},
		},
	})
	if err != nil {
		return gas.NotFound("file not found").WithCause(err)
	}
	defer obj.Body.Close() //nolint:errcheck // documentation snippet

	body, err := io.ReadAll(obj.Body)
	if err != nil {
		return err
	}
	return ctx.Text(http.StatusOK, string(body))
}

// #region mock
// Every provider ships a recording mock, so a unit test needs no backend and
// no network. Set only the methods the test exercises.
func TestDownloadUsesStorage(t *testing.T) {
	mock := &storagetest.MockProvider{}
	mock.GetObjectFn = func(_ context.Context, _ *storage.GetObjectInput) (*storage.Object, error) {
		return &storage.Object{
			Body:       io.NopCloser(strings.NewReader("hello")),
			ObjectInfo: storage.ObjectInfo{ContentType: "text/plain"},
		}, nil
	}

	svc := New(mock, gas.NewApp().Router())
	if svc.storage != storage.Provider(mock) {
		t.Fatal("service should hold the mock")
	}

	input := &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
		BaseObjectInput: storage.BaseObjectInput{Key: "notes.txt"},
	}}
	if _, err := mock.GetObject(context.Background(), input); err != nil {
		t.Fatalf("get object: %v", err)
	}
	if mock.CallCount("GetObject") != 1 {
		t.Fatalf("expected one GetObject call, got %d", mock.CallCount("GetObject"))
	}
}

// #endregion mock

// #region handler
// For an end-to-end test, drive the real handler stack. App.Handler returns the
// router behind CSRF protection, so the test exercises the same middleware as
// production without binding a port.
func TestDownloadRoute(t *testing.T) {
	mock := &storagetest.MockProvider{}
	mock.GetObjectFn = func(_ context.Context, _ *storage.GetObjectInput) (*storage.Object, error) {
		return &storage.Object{Body: io.NopCloser(strings.NewReader("hello"))}, nil
	}

	app := gas.NewApp(
		gas.WithServiceInstance[storage.Provider](mock),
		gas.WithSingletonService[*Service](New),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/files/notes.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // documentation snippet

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

// #endregion handler
