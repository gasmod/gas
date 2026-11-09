package ui_test

import (
	"context"
	"html/template"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

// KI-032: RenderFragment of a page that defines only its `content` block
// rendered an empty body with no error, since it executed the page's
// top-level text, and two tests pinned that as intended. It now executes
// the block, and the top-level text only when the page has no block.
func TestKI032_FragmentRendersContentBlock(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/base.html": `{{define "base"}}<main>{{block "content" .}}{{end}}</main>{{end}}`,
		"users.html":        `{{define "content"}}<ul>{{.}}</ul>{{end}}`,
		"solo.html":         `{{define "content"}}<p>{{.}}</p>{{end}}`,
	}, nil)

	assert.Equal(t, "<ul>u</ul>", fragmentBody(t, s, "users", "u"))
	assert.Equal(t, "<main><ul>u</ul></main>", body(t, s, "users", "u"))

	noLayout := initialized(t, files{"solo.html": `{{define "content"}}<p>{{.}}</p>{{end}}`}, nil)
	assert.Equal(t, "<p>s</p>", fragmentBody(t, noLayout, "solo", "s"))
}

// KI-053: one page that failed to compile made every render of every page
// fail, at the first build and after a RegisterFuncs, and the last good
// set was kept but never read. Now a page's compile error fails that
// page's renders only.
func TestKI053_OneBadPageFailsOnlyItself(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"good.html": `G`, "bad.html": `{{nope}}`}, nil)

	assert.Equal(t, "G", body(t, s, "good", nil))

	_, err := render(t, s, "bad", nil)
	require.ErrorIs(t, err, ui.ErrCompile)

	require.NoError(t, s.RegisterFuncs(template.FuncMap{"other": func() string { return "" }}))
	assert.Equal(t, "G", body(t, s, "good", nil))

	require.NoError(t, s.RegisterFuncs(template.FuncMap{"nope": func() string { return "fixed" }}))
	assert.Equal(t, "fixed", body(t, s, "bad", nil))
}

// A partial's or a layout's top-level text became the fragment of every
// page without its own, and any template could define the internal page
// root `__page__` to replace a page's fragment. Each file's top-level
// text is now a template of its own name, `__page__` is an ordinary name,
// and a name that ends in an extension cannot be defined.
func TestFragment_NoHijack(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"partials/stray.html": `PARTIAL-TEXT{{define "__page__"}}hijack{{end}}`,
		"layouts/base.html":   `LAYOUT-TEXT{{define "base"}}{{block "content" .}}{{end}}{{end}}`,
		"only.html":           `{{define "content"}}C{{end}}`,
		"top.html":            `T`,
		"empty.html":          `{{define "x"}}{{end}}`,
	}, nil)

	assert.Equal(t, "C", fragmentBody(t, s, "only", nil))
	assert.Equal(t, "T", fragmentBody(t, s, "top", nil))

	_, err := fragment(t, s, "empty", nil)
	require.ErrorIs(t, err, ui.ErrNoFragment)

	assert.Equal(t, "hijack", fragmentBody(t, s, "top", nil, ui.WithBlock("__page__")))
	assert.Equal(
		t,
		"PARTIAL-TEXT",
		fragmentBody(t, s, "top", nil, ui.WithBlock("partials/stray.html")),
	)
}

// WithBlock was ignored with layouts, and without them parsed into the
// shared template, so its second call failed with `cannot Parse after
// Execute` and concurrent calls raced. It now executes the named
// template of the compiled set, every time.
func TestWithBlock_Repeated(t *testing.T) {
	t.Parallel()

	for _, fs := range []files{
		{"p.html": `TOP{{define "a"}}A{{end}}`},
		{"p.html": `TOP{{define "a"}}A{{end}}`, "layouts/l.html": `{{define "base"}}L{{end}}`},
	} {
		s := initialized(t, fs, nil)

		for range 3 {
			assert.Equal(t, "A", fragmentBody(t, s, "p", nil, ui.WithBlock("a")))
		}

		assert.Equal(t, "TOP", fragmentBody(t, s, "p", nil))
	}
}

// An empty `ui.staticpath` with static files registered the route `""`,
// which made App.Start panic in the router. It is now a config error from
// Init, and the App does not start.
func TestStatic_EmptyStaticPathFailsInit(t *testing.T) {
	t.Parallel()

	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](gas.ConfigProvider(provider(t, map[string]any{
			"ui.staticdir": t.TempDir(), "ui.staticpath": "",
		}))),
		gas.WithServiceInstance[gas.TemplateProvider](gas.TemplateProvider(newStore(t, nil))),
		gas.WithSingletonService[*ui.Service](ui.New[gas.TemplateProvider]()),
	)

	require.NotPanics(t, func() {
		err := app.Start(t.Context())
		require.ErrorIs(t, err, ui.ErrInvalidConfig)
	})
}

// `ui.staticdir: "."` served the working directory with its dotfiles,
// such as `.env`. A directory is now opened as an os.Root wherever it is,
// and dotfiles are not served unless `ui.staticdotfiles` allows them.
//
// Not parallel: it changes the working directory.
func TestStatic_DotStaticDir(t *testing.T) {
	dir := staticTree(t)
	t.Chdir(dir)

	router := gas.NewRouter()

	s, err := ui.New[gas.TemplateProvider](
		ui.WithConfig(cfgWith(func(set *ui.Settings) { set.StaticDir = "." })),
	)(
		newStore(t, nil),
		router,
		nil,
		newRecorder(),
	)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	t.Chdir(filepath.Dir(dir))
	router.Seal()

	for path, code := range map[string]int{
		"/static/app.css": http.StatusOK, "/static/.env": http.StatusNotFound,
		"/static/.git/config": http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		assert.Equal(t, code, w.Code, path)
	}
}

// Concurrent renders that each needed a build each ran one when the
// build failed as a whole with no set to keep, as the store was down:
// 16 renders listed the store 16 times. A render that waited while a
// build finished now takes that build's outcome, in reload mode too.
func TestBuild_WaitersTakeTheFinishedBuild(t *testing.T) {
	t.Parallel()

	for _, reload := range []bool{false, true} {
		st := newFlaky(t, files{"p.html": `P`})
		s, _ := initializedLog(t, st, func(set *ui.Settings) { set.Reload = reload })

		const renders = 16

		var started, done sync.WaitGroup

		started.Add(renders)

		hold := func() {
			started.Wait()
			time.Sleep(100 * time.Millisecond)
		}
		st.onList.Store(&hold)

		down := errStoreDown
		st.listErr.Store(&down)

		for range renders {
			done.Go(func() {
				started.Done()

				_, err := render(t, s, "p", nil)
				assert.EqualError(t, err, `gas/ui: render "p": load templates failed: store down`)
			})
		}

		done.Wait()
		assert.Equal(t, int64(1), st.lists.Load(), "reload %v", reload)

		st.onList.Store(nil)
		st.listErr.Store(nil)
		assert.Equal(t, "P", body(t, s, "p", nil))
	}
}

// A page whose read failed once, such as a db timeout, failed every
// render until Reload, while the other pages rendered. It is now read
// again by the next render that needs it, without a new build.
func TestBuild_FailedPageReadAgain(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"p.html": `P`, "q.html": `Q`})
	s, log := initializedLog(t, st, nil)

	broken, name := errDisk, "p.html"
	st.getErrName.Store(&name)
	st.getErr.Store(&broken)

	_, err := render(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrLoad)
	require.ErrorIs(t, err, broken)
	require.EqualError(t, err, `gas/ui: render "p": load templates failed: "p.html": disk on fire`)

	_, err = render(t, s, "p", nil)
	require.EqualError(t, err, `gas/ui: render "p": load templates failed: "p.html": disk on fire`,
		"each render reads the page again")

	assert.Equal(t, "Q", body(t, s, "q", nil))
	require.NoError(t, s.CheckReady(t.Context()))

	st.getErr.Store(nil)
	assert.Equal(t, "P", body(t, s, "p", nil))
	assert.Equal(t, "P", fragmentBody(t, s, "p", nil))
	assert.Equal(t, int64(1), st.lists.Load(), "no new build")
	assert.Equal(t, []string{"template build failed"}, log.errorMessages())
}

// A page that compiled and then failed in a later build, to compile or
// to be read, failed its renders until it was fixed. Outside reload
// mode it now keeps its version of the build before; Reload returns the
// error and it is logged once. In reload mode a render shows the error.
func TestBuild_PageKeepsLastGood(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	st := newFlaky(t, files{"p.html": `P1`, "q.html": `Q1`})
	s, log := initializedLog(t, st, nil)

	assert.Equal(t, "P1", body(t, s, "p", nil))

	require.NoError(t, st.Register(ctx, "p.html", []byte(`{{nope}}`)))
	require.NoError(t, st.Register(ctx, "q.html", []byte(`Q2`)))

	err := s.Reload(ctx)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: reload: compile failed: template: p.html:1: function "nope" not defined`,
	)

	for range 3 {
		assert.Equal(t, "P1", body(t, s, "p", nil))
	}

	assert.Equal(t, "Q2", body(t, s, "q", nil))
	require.NoError(t, s.CheckReady(ctx))
	assert.Equal(t, []string{"template build failed"}, log.errorMessages())

	broken, name := errDisk, "p.html"
	st.getErrName.Store(&name)
	st.getErr.Store(&broken)

	require.ErrorIs(t, s.Reload(ctx), ui.ErrLoad)
	assert.Equal(t, "P1", body(t, s, "p", nil))

	st.getErr.Store(nil)
	require.NoError(t, st.Register(ctx, "p.html", []byte(`P3`)))
	require.NoError(t, s.Reload(ctx))
	assert.Equal(t, "P3", body(t, s, "p", nil))

	dev := initialized(t, files{"p.html": `{{nope}}`}, func(set *ui.Settings) { set.Reload = true })

	_, err = render(t, dev, "p", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
}

// A page that defines no fragment block but has top-level text rendered
// that text with RenderFragment and the layout's own block with Render.
// The text is now the block too, so both agree.
func TestFragment_TopLevelTextIsTheBlock(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{
		"layouts/base.html": `{{define "base"}}<main>{{block "content" .}}default{{end}}</main>{{end}}`,
		"text.html":         `<p>{{.}}</p>`,
		"blank.html":        `{{define "x"}}X{{end}}`,
	}, nil)

	assert.Equal(t, "<p>t</p>", fragmentBody(t, s, "text", "t"))
	assert.Equal(t, "<main><p>t</p></main>", body(t, s, "text", "t"))
	assert.Equal(t, "<main>default</main>", body(t, s, "blank", nil))
}

// The page's set was executed in place, so html/template escaped each
// template for the context of the first render that reached it, and a
// WithBlock that failed to escape left the template without a body: the
// next Render of the page panicked. Each template now runs in a copy of
// the set of its own, so both orders give the same result.
func TestRender_EscapeIsolatedPerTemplate(t *testing.T) {
	t.Parallel()

	fs := files{
		"layouts/base.html":  `{{define "base"}}{{template "open" .}}">link</a>{{block "content" .}}{{end}}{{end}}`,
		"partials/open.html": `{{define "open"}}<a href="/u/{{.}}{{end}}`,
		"p.html":             `{{define "content"}}<p>{{.}}</p>{{end}}`,
	}
	want := `<a href="/u/x">link</a><p>x</p>`
	// The text after the prefix is html/template's dump of its context,
	// which changes with Go releases.
	wantErr := `gas/ui: render fragment "p": execute failed: html/template:open: ends in a non-text context`
	escapeErr := func(t assert.TestingT, err error) bool {
		return assert.ErrorIs(t, err, ui.ErrExecute) && assert.ErrorContains(t, err, wantErr)
	}

	renderFirst := initialized(t, fs, nil)
	assert.Equal(t, want, body(t, renderFirst, "p", "x"))

	_, err := fragment(t, renderFirst, "p", "x", ui.WithBlock("open"))
	require.True(t, escapeErr(t, err))

	blockFirst := initialized(t, fs, nil)

	_, err = fragment(t, blockFirst, "p", "x", ui.WithBlock("open"))
	require.True(t, escapeErr(t, err))
	require.NotPanics(t, func() { assert.Equal(t, want, body(t, blockFirst, "p", "x")) })

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			assert.Equal(t, want, body(t, blockFirst, "p", "x"))

			_, ferr := fragment(t, blockFirst, "p", "x", ui.WithBlock("open"))
			escapeErr(t, ferr)
		})
	}

	wg.Wait()

	_, err = fragment(t, blockFirst, "p", "x", ui.WithBlock("gas/ui base"))
	require.EqualError(t, err, `gas/ui: render fragment "p": block not found: "gas/ui base"`,
		"the root of the layouts and partials has no body")
}

// A panic while a page executes, such as from an iterator the page
// ranges over, is returned as ErrExecute and writes nothing.
func TestRender_PanicIsExecuteError(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `{{range .}}{{.}}{{end}}`}, nil)

	var seq iter.Seq[int] = func(func(int) bool) { panic("iterator broke") }

	w := newSpy()

	var err error

	require.NotPanics(t, func() { err = s.Render(t.Context(), w, "p", seq) })
	require.ErrorIs(t, err, ui.ErrExecute)
	require.EqualError(t, err, `gas/ui: render "p": execute failed: panic: iterator broke`)
	assert.True(t, w.untouched())
	assert.Equal(t, "ok", body(t, s, "p", []string{"ok"}))
}

// A render that read a failed page again held the page's lock while the
// store read, and a build took the same lock to keep the page's last good
// version, so a read that hung stopped every render after a
// RegisterFuncs or a Reload. The read now runs without the lock: other
// pages render promptly while it hangs.
func TestBuild_HangingRereadBlocksNoOtherPage(t *testing.T) {
	t.Parallel()

	st := newFlaky(t, files{"a.html": `A`, "b.html": `B`})
	s, _ := initializedLog(t, st, nil)

	broken, name := errDisk, "b.html"
	st.getErrName.Store(&name)
	st.getErr.Store(&broken)

	assert.Equal(t, "A", body(t, s, "a", nil))
	st.getErr.Store(nil)

	release, entered := make(chan struct{}), make(chan struct{})
	hang := hangOn(name, entered, release)
	st.getHook.Store(&hang)

	var reader sync.WaitGroup

	reader.Go(func() {
		_, err := render(t, s, "b", nil)
		assert.NoError(t, err, "the read resumes once released")
	})

	<-entered

	failFast := failOn(name)
	st.getHook.Store(&failFast)

	var (
		regErr, reloadErr error
		w1, w2            *httptest.ResponseRecorder
		r1Err, r2Err      error
	)

	prompt(t, "a render after RegisterFuncs", func() {
		regErr = s.RegisterFuncs(template.FuncMap{"f": func() string { return "" }})
		w1 = httptest.NewRecorder()
		r1Err = s.Render(t.Context(), w1, "a", nil)
	})
	prompt(t, "Reload", func() { reloadErr = s.Reload(t.Context()) })
	prompt(t, "a render after Reload", func() {
		w2 = httptest.NewRecorder()
		r2Err = s.Render(t.Context(), w2, "a", nil)
	})

	require.NoError(t, regErr)
	require.NoError(t, r1Err)
	require.ErrorIs(t, reloadErr, ui.ErrLoad)
	require.NoError(t, r2Err)
	assert.Equal(t, "A", w1.Body.String())
	assert.Equal(t, "A", w2.Body.String())

	st.getHook.Store(nil)
	close(release)
	reader.Wait()
}

// hangOn returns a Get hook that, for the template name, closes entered
// and waits for release or the end of the read's context.
func hangOn(name string, entered, release chan struct{}) func(context.Context, string) error {
	return func(ctx context.Context, n string) error {
		if n != name {
			return nil
		}

		close(entered)

		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// failOn returns a Get hook that fails the template name with errDisk.
func failOn(name string) func(context.Context, string) error {
	return func(_ context.Context, n string) error {
		if n == name {
			return errDisk
		}

		return nil
	}
}

// prompt runs fn and fails the test when it does not return within 5s.
func prompt(t *testing.T, what string, fn func()) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		defer close(done)

		fn()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked behind the hanging read", what)
	}
}

// A page without the fragment block whose top-level text calls the
// block itself had that text as the block too, so it called itself
// until html/template's depth limit; and a partial's definition of the
// block was replaced by the page's text. The text is now the block only
// when neither holds.
func TestFragment_NoAliasOverSelfCallOrPartial(t *testing.T) {
	t.Parallel()

	layout := `{{define "base"}}<main>{{block "content" .}}L{{end}}</main>{{end}}`

	cases := []struct {
		fs       files
		render   string
		fragment string
	}{
		{
			fs: files{
				"layouts/base.html": layout,
				"p.html":            `<p>{{template "content" .}}</p>`,
			},
			render:   "<main>L</main>",
			fragment: "<p>L</p>",
		},
		{
			fs: files{
				"layouts/base.html": layout,
				"p.html":            `{{if .}}{{range .}}{{with .}}<p>{{template "content" .}}</p>{{end}}{{end}}{{end}}`,
			},
			render:   "<main>L</main>",
			fragment: "<p>L</p>",
		},
		{
			fs: files{
				"partials/c.html": `{{define "content"}}PARTIAL{{end}}`,
				"p.html":          `<p>{{template "content" .}}</p>`,
			},
			render:   "<p>PARTIAL</p>",
			fragment: "<p>PARTIAL</p>",
		},
		{
			fs: files{
				"layouts/base.html": layout,
				"partials/c.html":   `{{define "content"}}PARTIAL{{end}}`,
				"p.html":            `<p>x</p>`,
			},
			render:   "<main>PARTIAL</main>",
			fragment: "<p>x</p>",
		},
	}

	for _, c := range cases {
		s := initialized(t, c.fs, nil)

		assert.Equal(t, c.render, body(t, s, "p", []string{"x"}), c.fs["p.html"])
		assert.Equal(t, c.fragment, fragmentBody(t, s, "p", []string{"x"}), c.fs["p.html"])
	}
}

// A page whose text called the fragment block through another template,
// such as {{template "base" .}} calling the layout, had that text as the
// block too, so the layout called it until html/template's depth limit.
// Calling the block through the layout, a partial or the page's own
// define now counts as calling it, and a cycle of templates that never
// calls it leaves the text the block.
func TestFragment_NoAliasOverIndirectCall(t *testing.T) {
	t.Parallel()

	layout := `{{define "base"}}<main>{{block "content" .}}L{{end}}</main>{{end}}`

	cases := []struct {
		fs       files
		render   string
		fragment string
	}{
		{
			fs: files{
				"layouts/base.html": layout,
				"p.html":            `{{template "base" .}}`,
			},
			render:   "<main>L</main>",
			fragment: "<main>L</main>",
		},
		{
			fs: files{
				"layouts/base.html": layout,
				"partials/w.html":   `{{define "wrap"}}<div>{{template "base" .}}</div>{{end}}`,
				"p.html":            `{{if .}}{{template "wrap" .}}{{end}}`,
			},
			render:   "<main>L</main>",
			fragment: "<div><main>L</main></div>",
		},
		{
			fs: files{
				"layouts/base.html": layout,
				"p.html":            `{{define "inner"}}<i>{{template "content" .}}</i>{{end}}<p>{{template "inner" .}}</p>`,
			},
			render:   "<main>L</main>",
			fragment: "<p><i>L</i></p>",
		},
		{
			fs: files{
				"layouts/base.html": layout,
				"partials/a.html":   `{{define "a"}}{{if not .}}{{template "b" .}}{{end}}{{end}}`,
				"partials/b.html":   `{{define "b"}}{{if not .}}{{template "a" .}}{{end}}{{end}}`,
				"p.html":            `<p>{{template "a" .}}</p>`,
			},
			render:   "<main><p></p></main>",
			fragment: "<p></p>",
		},
	}

	for _, c := range cases {
		s := initialized(t, c.fs, nil)

		assert.Equal(t, c.render, body(t, s, "p", []string{"x"}), c.fs["p.html"])
		assert.Equal(t, c.fragment, fragmentBody(t, s, "p", []string{"x"}), c.fs["p.html"])
	}
}

// Without a layout or a partial that defines it, a page whose text calls
// the fragment block fails to execute both ways, with the template
// missing, instead of calling itself.
func TestFragment_SelfCallWithoutLayout(t *testing.T) {
	t.Parallel()

	s := initialized(t, files{"p.html": `<p>{{template "content" .}}</p>`}, nil)

	_, err := render(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrExecute)
	assert.Contains(t, err.Error(), `no such template "content"`)

	_, err = fragment(t, s, "p", nil)
	require.ErrorIs(t, err, ui.ErrExecute)
	assert.Contains(t, err.Error(), `no such template "content"`)
}
