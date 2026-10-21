package index_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Docs: index.mdx#_top
// "That is a complete Gas app."
// The quickstart app, driven through Start and Handler instead of Run so the
// test binds no port.
func TestQuickstartIsACompleteApp(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "Hello, World!" {
		t.Fatalf("got %d %q, want 200 %q", resp.StatusCode, body, "Hello, World!")
	}
}

// Docs: index.mdx#_top
// "An app that just serves HTTP depends on the core package alone."
func TestHTTPOnlyAppDependsOnCoreAlone(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}}",
		"github.com/gasmod/gas/docs/snippets/quickstart").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var gasModules []string
	for _, mod := range strings.Fields(string(out)) {
		if strings.HasPrefix(mod, "github.com/gasmod/") && !slices.Contains(gasModules, mod) {
			gasModules = append(gasModules, mod)
		}
	}
	slices.Sort(gasModules)
	want := []string{"github.com/gasmod/gas", "github.com/gasmod/gas/docs/snippets"}
	if !slices.Equal(gasModules, want) {
		t.Fatalf("quickstart pulls in Gas modules %v, want only %v", gasModules, want)
	}
}

// Docs: index.mdx#_top
// "A service is three methods: `Name`, `Init`, `Close`. The container resolves constructors, sorts dependencies, and manages the lifecycle."
func TestContainerSortsDependenciesAndManagesLifecycle(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	// Registered dependent first, so only sorting puts kB before kA.
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func(*svc[kB]) *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return &svc[kB]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	got := rec.list()
	want := []string{"init kB", "init kA", "close kA", "close kB"}
	if !slices.Equal(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
}

type fakeCache struct{}

func (fakeCache) Get(context.Context, string) ([]byte, error)              { return []byte("mine"), nil }
func (fakeCache) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (fakeCache) Delete(context.Context, string) error                     { return nil }
func (fakeCache) Exists(context.Context, string) (bool, error)             { return true, nil }

// Docs: index.mdx#_top
// "Modules meet through provider interfaces declared in core, so any of them swaps for your own implementation or a test mock."
func TestProviderInterfaceSwapsForOwnImplementation(t *testing.T) {
	t.Parallel()
	var got gas.CacheProvider
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.CacheProvider](fakeCache{}),
		gas.WithSingletonService[*svc[kA]](func(c gas.CacheProvider) *svc[kA] { got = c; return &svc[kA]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if _, ok := got.(fakeCache); !ok {
		t.Fatalf("consumer got %T, want the swapped-in fakeCache", got)
	}
}

type halfService struct{}

func (halfService) Init(context.Context) error { return nil }

type unregistered struct{}

// Docs: index.mdx#_top
// "Unresolved dependencies, malformed services, and handler signature mismatches are all rejected before the server accepts traffic."
// Assumption: "rejected" is satisfied by Start returning an error or by a
// panic before Start returns; either keeps the server from being reached.
func TestMisconfigurationRejectedBeforeTraffic(t *testing.T) {
	t.Parallel()
	cases := map[string]func() *gas.App{
		"unresolved dependency": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*svc[kA]](func(*unregistered) *svc[kA] { return &svc[kA]{} }))
		},
		"malformed service": func() *gas.App {
			return gas.NewApp(gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
		},
		"handler declares an unregistered parameter": func() *gas.App {
			app := gas.NewApp()
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		},
		"handler signature mismatch": func() *gas.App {
			app := gas.NewApp()
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) string { return "" })
			return app
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = errors.New("panicked")
					}
				}()
				app := build()
				if err := app.Start(t.Context()); err != nil {
					return err
				}
				_ = app.Stop(context.Background())
				return nil
			}()
			if err == nil {
				t.Fatal("the app started; want it rejected")
			}
		})
	}
}
