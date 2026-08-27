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
	"github.com/gasmod/gas/storage/storagetest"
)

// #endregion imports

// The service under test depends on the interface, not on a backend.
type Service struct {
	storage gas.StorageProvider
	router  *gas.Router
}

func New(storage gas.StorageProvider, router *gas.Router) *Service {
	return &Service{storage: storage, router: router}
}

func (s *Service) Name() string { return "files" }
func (s *Service) Close() error { return nil }

func (s *Service) Init() error {
	s.router.Handle(s.Name(), http.MethodGet, "/files/{key}", s.download)
	return nil
}

func (s *Service) download(ctx gas.Context) error {
	obj, err := s.storage.Download(ctx, ctx.Param("key"))
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
	mock := &storagetest.MockStorage{}
	mock.DownloadFn = func(_ context.Context, key string, _ ...gas.StorageOption) (*gas.StorageObject, error) {
		return &gas.StorageObject{
			Body:        io.NopCloser(strings.NewReader("hello")),
			ContentType: "text/plain",
		}, nil
	}

	svc := New(mock, gas.NewApp().Router())
	if svc.storage != gas.StorageProvider(mock) {
		t.Fatal("service should hold the mock")
	}

	if _, err := mock.Download(context.Background(), "notes.txt"); err != nil {
		t.Fatalf("download: %v", err)
	}
	if mock.CallCount("Download") != 1 {
		t.Fatalf("expected one Download call, got %d", mock.CallCount("Download"))
	}
}

// #endregion mock

// #region handler
// For an end-to-end test, drive the real handler stack. App.Handler returns the
// router behind CSRF protection, so the test exercises the same middleware as
// production without binding a port.
func TestDownloadRoute(t *testing.T) {
	mock := &storagetest.MockStorage{}
	mock.DownloadFn = func(_ context.Context, _ string, _ ...gas.StorageOption) (*gas.StorageObject, error) {
		return &gas.StorageObject{Body: io.NopCloser(strings.NewReader("hello"))}, nil
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.StorageProvider](mock),
		gas.WithSingletonService[*Service](New),
	)
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })

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
