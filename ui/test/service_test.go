package ui_test

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// flaky is a gas.TemplateProvider over a memory store whose List and Get
// fail while their errors are set, Get only for getErrName when it is
// set, whose Get first calls getHook when it is set and returns its
// error, and that counts its calls. It is safe for concurrent use.
type flaky struct {
	store      *memory.Store
	listErr    atomic.Pointer[error]
	getErr     atomic.Pointer[error]
	getErrName atomic.Pointer[string]
	onGet      atomic.Pointer[func()]
	onList     atomic.Pointer[func()]
	getHook    atomic.Pointer[func(ctx context.Context, name string) error]
	lists      atomic.Int64
	gets       atomic.Int64
}

var _ gas.TemplateProvider = (*flaky)(nil)

// newFlaky returns a flaky store holding fs.
func newFlaky(t *testing.T, fs files) *flaky {
	t.Helper()

	return &flaky{store: newStore(t, fs)}
}

// Get returns getErr while set, else the store's template.
func (f *flaky) Get(ctx context.Context, name string) ([]byte, error) {
	f.gets.Add(1)

	if fn := f.onGet.Load(); fn != nil {
		(*fn)()
	}

	if hook := f.getHook.Load(); hook != nil {
		err := (*hook)(ctx, name)
		if err != nil {
			return nil, err
		}
	}

	if err := f.getErr.Load(); err != nil {
		if only := f.getErrName.Load(); only == nil || *only == name {
			return nil, *err
		}
	}

	err := ctx.Err()
	if err != nil {
		return nil, err
	}

	return f.store.Get(ctx, name)
}

// List returns listErr while set, else the store's names.
func (f *flaky) List(ctx context.Context) ([]string, error) {
	f.lists.Add(1)

	if fn := f.onList.Load(); fn != nil {
		(*fn)()
	}

	if err := f.listErr.Load(); err != nil {
		return nil, *err
	}

	return f.store.List(ctx)
}

// Register registers in the store.
func (f *flaky) Register(ctx context.Context, name string, content []byte) error {
	return f.store.Register(ctx, name, content)
}

// RegisterFS registers in the store.
func (f *flaky) RegisterFS(ctx context.Context, fsys fs.FS) error {
	return f.store.RegisterFS(ctx, fsys)
}

// The constructor refuses a nil logger and a nil store, typed nil
// pointers included, where it used to panic or fail at the first render.
func TestNew_NilDependencies(t *testing.T) {
	t.Parallel()

	_, err := ui.New[gas.TemplateProvider]()(newStore(t, nil), nil, nil, nil)
	require.ErrorIs(t, err, ui.ErrNilLogger)
	require.EqualError(t, err, "gas/ui: logger is nil")

	_, err = ui.New[gas.TemplateProvider]()(nil, nil, nil, newRecorder())
	require.ErrorIs(t, err, ui.ErrNilTemplateProvider)
	require.EqualError(t, err, "gas/ui: template provider is nil")

	_, err = ui.New[*memory.Store]()(nil, nil, nil, newRecorder())
	require.ErrorIs(t, err, ui.ErrNilTemplateProvider)

	s, err := ui.New[*memory.Store]()(newStore(t, nil), nil, nil, newRecorder())
	require.NoError(t, err)
	assert.Equal(t, ui.ServiceName, s.Name())
}

// Init needs WithConfig or a ConfigProvider, refuses WithConfig(nil),
// and refuses static files without a router or a nil WithStaticFS.
func TestInit_Dependencies(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	s, _ := build(t, newStore(t, nil))
	err := s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/ui: init: no config provider")

	s, _ = build(t, newStore(t, nil), ui.WithConfig(nil))
	err = s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrNilConfig)
	require.EqualError(t, err, "gas/ui: init: config is nil")

	s, _ = build(t, newStore(t, nil), ui.WithConfig(cfgWith(func(set *ui.Settings) {
		set.StaticDir = t.TempDir()
	})))
	err = s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrNilRouter)
	require.EqualError(t, err, "gas/ui: init: router is nil")

	s, _ = build(t, newStore(t, nil), ui.WithConfig(cfgWith(nil)), ui.WithStaticFS(nil))
	err = s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrNilFS)
	require.EqualError(t, err, "gas/ui: init: file system is nil")

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	s, _ = build(t, newStore(t, nil), ui.WithConfig(cfgWith(nil)))
	err = s.Init(canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/ui: init: context canceled")
}

// Before the first Init every method but Init, Close and Name returns
// ErrNotInitialized and writes nothing; Close returns nil and logs
// nothing.
func TestService_BeforeInit(t *testing.T) {
	t.Parallel()

	s, log := build(t, newStore(t, files{"p.html": `P`}), ui.WithConfig(cfgWith(nil)))
	ctx := t.Context()

	w := newSpy()
	err := s.Render(ctx, w, "p", nil)
	require.ErrorIs(t, err, ui.ErrNotInitialized)
	require.EqualError(t, err, `gas/ui: render "p": service not initialized`)
	assert.True(t, w.untouched())

	err = s.RenderFragment(ctx, w, "p", nil)
	require.EqualError(t, err, `gas/ui: render fragment "p": service not initialized`)
	assert.True(t, w.untouched())

	require.EqualError(
		t,
		s.RegisterFuncs(template.FuncMap{}),
		"gas/ui: register funcs: service not initialized",
	)
	require.EqualError(t, s.Reload(ctx), "gas/ui: reload: service not initialized")
	require.EqualError(t, s.CheckReady(ctx), "gas/ui: ready: service not initialized")
	require.NoError(t, s.Close(ctx))
	assert.Empty(t, log.entries())
}

// A Render on a closed service writes nothing and returns ErrClosed, so a
// handler that returns the error has the response written once, by the
// error handler, where it used to get a 503 body and then the error
// handler's.
func TestService_ClosedRenderWritesOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _ := build(t, newStore(t, files{"p.html": `P`}), ui.WithConfig(cfgWith(nil)))
	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Close(ctx))

	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}))
	app.Router().Handle(nil, http.MethodGet, "/p", func(c gas.Context) error {
		return s.Render(c.Request().Context(), c.ResponseWriter(), "p", nil)
	})
	require.NoError(t, app.Start(ctx))

	defer func() { require.NoError(t, app.Stop(context.Background())) }()

	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/p", nil))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "unavailable")
	assert.JSONEq(
		t,
		`{"title":"Internal Server Error","status":500,"detail":"internal server error","code":"internal_error"}`,
		w.Body.String(),
	)
}

// Init logs `service initialized` with whether reload mode and static
// files are on, and Close logs `service closed`, both at info level.
func TestService_Logs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, log := build(
		t,
		newStore(t, nil),
		ui.WithConfig(cfgWith(func(set *ui.Settings) { set.Reload = true })),
	)
	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Close(ctx))

	e := log.entries()
	require.Len(t, e, 2)
	assert.Equal(t, entry{level: "info", msg: "service initialized", fields: map[string]any{
		"service": ui.ServiceName, "reload": true, "static": false,
	}}, e[0])
	assert.Equal(t, entry{level: "info", msg: "service closed", fields: map[string]any{
		"service": ui.ServiceName,
	}}, e[1])
}

// Close then Init restarts the service, with the configuration resolved
// again and the functions given to RegisterFuncs kept, where Close was
// once permanent and a second Init lost them.
func TestService_Restart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cfg := cfgWith(nil)
	s, _ := build(
		t,
		newStore(t, files{"p.html": `{{shout .}}`, "layouts/l.html": `{{define "main"}}M{{end}}`}),
		ui.WithConfig(cfg),
	)
	require.NoError(t, s.Init(ctx))
	require.NoError(
		t,
		s.RegisterFuncs(template.FuncMap{"shout": func(v string) string { return v + "!" }}),
	)

	_, err := render(t, s, "p", "a", ui.WithLayout("main"))
	require.NoError(t, err)

	cfg.UI.LayoutName = "main"

	require.NoError(t, s.Close(ctx))
	require.NoError(t, s.Close(ctx))

	err = s.RegisterFuncs(template.FuncMap{})
	require.ErrorIs(t, err, ui.ErrClosed)
	require.EqualError(t, err, "gas/ui: register funcs: service closed")
	require.EqualError(t, s.Reload(ctx), "gas/ui: reload: service closed")

	require.NoError(t, s.Init(ctx))
	assert.Equal(t, "M", body(t, s, "p", "b"))
	assert.Equal(t, "b!", fragmentBody(t, s, "p", "b"))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	err = s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrAlreadyInitialized)
	require.EqualError(t, err, "gas/ui: init: service already initialized")
}

// Renders that race restarts complete or return ErrClosed, never
// ErrNotInitialized, and -race finds no data race on the service's state,
// which Init once replaced under running renders.
func TestService_RestartRace(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _ := build(t, newStore(t, files{"p.html": `P`}), ui.WithConfig(cfgWith(nil)))
	require.NoError(t, s.Init(ctx))

	var (
		wg   sync.WaitGroup
		stop atomic.Bool
	)

	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				err := s.Render(ctx, newSpy(), "p", nil)
				if err != nil && !errors.Is(err, ui.ErrClosed) {
					t.Errorf("render: %v", err)
				}

				err = s.CheckReady(ctx)
				if err != nil && !errors.Is(err, ui.ErrClosed) {
					t.Errorf("ready: %v", err)
				}
			}
		})
	}

	for range 50 {
		require.NoError(t, s.Close(ctx))
		require.NoError(t, s.Init(ctx))
	}

	stop.Store(true)
	wg.Wait()
	require.NoError(t, s.Close(ctx))
}

// Templates compile on the first render, not in Init, so functions
// dependents register in their own Init are in; concurrent first renders
// build once.
func TestService_LazyBuild(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"p.html": `{{dep}}`, "q.html": `Q`})
	s, _ := initializedLog(t, st, nil)

	assert.Zero(t, st.lists.Load())

	require.NoError(t, s.RegisterFuncs(template.FuncMap{"dep": func() string { return "D" }}))

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() { assert.Equal(t, "D", body(t, s, "p", nil)) })
	}

	wg.Wait()
	assert.Equal(t, int64(1), st.lists.Load())
	assert.Equal(t, int64(2), st.gets.Load(), "one Get per template per build")

	assert.Equal(t, "Q", body(t, s, "q", nil))
	require.NoError(t, s.CheckReady(t.Context()))
	assert.Equal(t, int64(1), st.lists.Load(), "CheckReady builds only when no build was tried")
}

// Outside reload mode, a change of the store is seen on Reload only; in
// reload mode, every render compiles again.
func TestService_StoreChanges(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	for _, reload := range []bool{false, true} {
		st := newFlaky(t, files{"a.html": `old`})
		s, _ := initializedLog(t, st, func(set *ui.Settings) { set.Reload = reload })

		assert.Equal(t, "old", body(t, s, "a", nil))
		require.NoError(t, st.Register(ctx, "a.html", []byte(`new`)))
		require.NoError(t, st.Register(ctx, "b.html", []byte(`B`)))

		if !reload {
			assert.Equal(t, "old", body(t, s, "a", nil))

			_, err := render(t, s, "b", nil)
			require.ErrorIs(t, err, ui.ErrPageNotFound)
			require.NoError(t, s.Reload(ctx))
		}

		assert.Equal(t, "new", body(t, s, "a", nil))
		assert.Equal(t, "B", body(t, s, "b", nil))
	}
}

// A build that fails as a whole keeps the set before it for renders;
// Reload returns the error, CheckReady reports ready as renders have a
// set, and an error-level log says so once, not on every render, until a
// build succeeds.
func TestService_FailedRebuildKeepsLastGood(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := newFlaky(t, files{"a.html": `A`})
	s, log := initializedLog(t, st, nil)

	assert.Equal(t, "A", body(t, s, "a", nil))

	down := errStoreDown
	st.listErr.Store(&down)

	err := s.Reload(ctx)
	require.ErrorIs(t, err, ui.ErrLoad)
	require.ErrorIs(t, err, down)
	require.EqualError(t, err, "gas/ui: reload: load templates failed: store down")
	require.NoError(t, s.CheckReady(ctx))

	require.NoError(t, s.RegisterFuncs(template.FuncMap{"f": func() string { return "" }}))

	for range 3 {
		assert.Equal(t, "A", body(t, s, "a", nil))
	}

	assert.Equal(
		t,
		[]string{"template build failed", "template build failed"},
		log.errorMessages(),
	)

	st.listErr.Store(nil)
	require.NoError(t, s.Reload(ctx))
	require.NoError(t, s.CheckReady(ctx))
}

// Without a set to keep, a failed build fails the render and is tried
// again on the next one; the error of a template that cannot be read
// wraps ErrLoad and the store's error and names the template.
func TestService_FailedFirstBuild(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"layouts/l.html": `{{define "base"}}L{{end}}`, "a.html": `A`})
	s, log := initializedLog(t, st, nil)

	broken := errDisk
	st.getErr.Store(&broken)

	_, err := render(t, s, "a", nil)
	require.ErrorIs(t, err, ui.ErrLoad)
	require.ErrorIs(t, err, broken)
	require.EqualError(
		t,
		err,
		`gas/ui: render "a": load templates failed: "layouts/l.html": disk on fire`,
	)
	require.EqualError(t, s.CheckReady(t.Context()),
		`gas/ui: ready: load templates failed: "layouts/l.html": disk on fire`)
	assert.Empty(t, log.errorMessages())

	st.getErr.Store(nil)
	assert.Equal(t, "L", body(t, s, "a", nil))
	require.NoError(t, s.CheckReady(t.Context()))
}

// A layout that fails to compile fails every page, as a whole build.
func TestService_BrokenLayout(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"layouts/l.html": `{{define "base"}}{{end`, "a.html": `A`}, nil)

	_, err := render(t, s, "a", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render "a": compile failed: template: layouts/l.html:1: unclosed action`,
	)
}

// A build that the render's context ends is not recorded: the next
// render builds again, and CheckReady does not report it.
func TestService_CanceledBuild(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"a.html": `A`})
	s, log := initializedLog(t, st, nil)

	assert.Equal(t, "A", body(t, s, "a", nil))
	require.NoError(t, st.Register(t.Context(), "a.html", []byte(`B`)))

	for _, reload := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		stop := func() { cancel() }
		st.onGet.Store(&stop)

		var err error

		if reload {
			err = s.Reload(ctx)
		} else {
			require.NoError(t, s.RegisterFuncs(template.FuncMap{"f": func() string { return "" }}))

			err = s.Render(ctx, newSpy(), "a", nil)
		}

		require.ErrorIs(t, err, context.Canceled)
		require.NoError(t, s.CheckReady(t.Context()))
		st.onGet.Store(nil)
	}

	assert.Equal(t, "B", body(t, s, "a", nil))
	assert.Empty(t, log.errorMessages())
}

// In reload mode, a failed build fails each render with its error and
// logs nothing.
func TestService_ReloadModeErrors(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"a.html": `A`})
	s, log := initializedLog(t, st, func(set *ui.Settings) { set.Reload = true })

	assert.Equal(t, "A", body(t, s, "a", nil))

	down := errStoreDown
	st.listErr.Store(&down)

	_, err := render(t, s, "a", nil)
	require.ErrorIs(t, err, down)
	assert.Empty(t, log.errorMessages())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = fragment(t, s, "a", nil)
	require.ErrorIs(t, err, down)

	err = s.RenderFragment(ctx, newSpy(), "a", nil)
	require.ErrorIs(t, err, context.Canceled)
}

// The static route of a service with a static fs.FS serves its files
// through an App, answers 503 while the service is closed, and serves
// again after a restart.
func TestService_StaticThroughApp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	fsys := fstest.MapFS{"app.css": {Data: []byte("body{}")}}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](gas.ConfigProvider(config.New())),
		gas.WithServiceInstance[gas.TemplateProvider](
			gas.TemplateProvider(newStore(t, files{"a.html": `A`})),
		),
		gas.WithSingletonService[*ui.Service](ui.New[gas.TemplateProvider](
			ui.WithConfig(cfgWith(nil)), ui.WithStaticFS(fsys))),
	)
	app.Router().Handle(nil, http.MethodGet, "/a", func(c gas.Context, u *ui.Service) error {
		return u.Render(c.Request().Context(), c.ResponseWriter(), "a", nil)
	})
	require.NoError(t, app.Start(ctx))

	defer func() { require.NoError(t, app.Stop(context.Background())) }()

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))

		return w
	}

	assert.Equal(t, "A", get("/a").Body.String())
	assert.Equal(t, "body{}", get("/static/app.css").Body.String())
	assert.Equal(t, "nosniff", get("/static/app.css").Header().Get("X-Content-Type-Options"))

	require.NoError(t, app.CloseService[*ui.Service](ctx))
	assert.Equal(t, http.StatusServiceUnavailable, get("/static/app.css").Code)

	require.NoError(t, app.RestartService[*ui.Service](ctx))
	assert.Equal(t, "body{}", get("/static/app.css").Body.String())
	assert.Equal(t, "A", get("/a").Body.String())
}

// A static route an earlier Init registered answers 404 while the
// current Init serves no static files, and 503 after a Close without an
// app.
func TestService_StaticRoute(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	router := gas.NewRouter()
	log := newRecorder()
	cfg := cfgWith(func(set *ui.Settings) { set.StaticDir = staticTree(t) })

	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(newStore(t, nil), router, nil, log)
	require.NoError(t, err)
	require.NoError(t, s.Init(ctx))

	router.Seal()

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))

		return w
	}

	assert.Equal(t, http.StatusOK, get("/static/app.css").Code)

	require.NoError(t, s.Close(ctx))

	w := get("/static/app.css")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))

	cfg.UI.StaticDir = ""

	require.NoError(t, s.Init(ctx))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	w = get("/static/app.css")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

// A file error the static route answers with 500 is logged at error
// level, with the path and the error.
func TestService_StaticErrorLogged(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	log := newRecorder()
	broken := errDisk
	r2 := gas.NewRouter()

	s2, err := ui.New[gas.TemplateProvider](
		ui.WithConfig(cfgWith(nil)),
		ui.WithStaticFS(errFS{err: broken}),
	)(
		newStore(t, nil),
		r2,
		nil,
		log,
	)
	require.NoError(t, err)
	require.NoError(t, s2.Init(ctx))

	defer func() { require.NoError(t, s2.Close(context.Background())) }()

	r2.Seal()

	w := httptest.NewRecorder()
	r2.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/static/f", nil))
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var logged []entry

	for _, e := range log.entries() {
		if e.level == "error" {
			logged = append(logged, e)
		}
	}

	require.Len(t, logged, 1)
	assert.Equal(t, "static file failed", logged[0].msg)
	assert.Equal(t, "/static/f", logged[0].fields["path"])
	require.ErrorIs(t, logged[0].fields["error"].(error), broken)
}

// CheckReady runs the first build when none was tried, within its own
// context, and fails only when no set renders can use exists: a page
// that fails to compile does not fail it. Once a build was tried it
// builds nothing.
func TestService_CheckReadyBuildsFirst(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := newFlaky(t, files{"good.html": `G`, "bad.html": `{{nope}}`})
	s, _ := initializedLog(t, st, nil)

	canceled, cancel := context.WithCancel(ctx)
	stop := func() { cancel() }
	st.onGet.Store(&stop)

	err := s.CheckReady(canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/ui: ready: context canceled")
	st.onGet.Store(nil)

	require.NoError(t, s.CheckReady(ctx))
	assert.Equal(t, int64(2), st.lists.Load())
	require.NoError(t, s.CheckReady(ctx))
	assert.Equal(t, int64(2), st.lists.Load())
	assert.Equal(t, "G", body(t, s, "good", nil))

	down := errStoreDown
	dead := newFlaky(t, files{"good.html": `G`})
	dead.listErr.Store(&down)

	d, _ := initializedLog(t, dead, func(set *ui.Settings) { set.Reload = true })

	require.EqualError(t, d.CheckReady(ctx), "gas/ui: ready: load templates failed: store down")
	require.EqualError(t, d.CheckReady(ctx), "gas/ui: ready: load templates failed: store down")
	assert.Equal(t, int64(1), dead.lists.Load(), "a failed build was tried")
}
