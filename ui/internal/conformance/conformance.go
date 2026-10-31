package conformance

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	texttemplate "text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	tmpl "github.com/gasmod/gas/template"
	"github.com/gasmod/gas/ui"
)

// Subject is a template store the suite runs the service over.
type Subject struct {
	// New returns a new store, ready for use, holding the files of seed
	// under their paths, and taking the extensions `.html` and `.txt`.
	New func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider
}

// The names the seeds share.
const (
	// layoutFile is the store name of the layout.
	layoutFile = "layouts/base.html"
	// pageFile is the store name of the page `p`.
	pageFile = "p.html"
	// layoutName is the name of the layout's template.
	layoutName = "base"
	// unclean is the reason of a page name that is not a clean path.
	unclean = "not a clean slash path"
	// goodFunc names the function of testRegisterFuncs.
	goodFunc = "good"
	// contentBlock is the name of the fragment block.
	contentBlock = "content"
)

// site is the seed of most rules: a layout, two partials and pages.
var site = fstest.MapFS{
	layoutFile: {
		Data: []byte(`{{define "base"}}<html>{{block "content" .}}default{{end}}` +
			`{{template "nav" .}}</html>{{end}}`),
	},
	"partials/nav.html":   {Data: []byte(`STRAY{{define "nav"}}<nav>{{.}}</nav>{{end}}`)},
	"partials/aside.html": {Data: []byte(`{{define "aside"}}<aside>{{upper .}}</aside>{{end}}`)},
	"home.html":           {Data: []byte(`{{define "content"}}<h1>{{.}}</h1>{{end}}`)},
	"plain.html":          {Data: []byte(`TOP {{.}}`)},
	"empty.html":          {Data: []byte(`{{define "other"}}x{{end}}`)},
	"admin/users.html":    {Data: []byte(`{{define "content"}}<ul>{{.}}</ul>{{end}}`)},
	"bad.html":            {Data: []byte(`{{define "content"}}{{.Missing}`)},
	"mail/welcome.txt":    {Data: []byte(`{{.Name`)},
}

// Run runs every rule of the suite on subject, as described in the
// package documentation.
func Run(t *testing.T, subject Subject) {
	t.Helper()

	rules := []struct {
		run  func(*testing.T, Subject)
		name string
	}{
		{name: "Layout", run: testLayout},
		{name: "Fragment", run: testFragment},
		{name: "NoLayouts", run: testNoLayouts},
		{name: "WithBlock", run: testWithBlock},
		{name: "EscapeOrder", run: testEscapeOrder},
		{name: "EscapeAlone", run: testEscapeAlone},
		{name: "Names", run: testNames},
		{name: "Isolation", run: testIsolation},
		{name: "ReservedNames", run: testReservedNames},
		{name: "Writes", run: testWrites},
		{name: "NothingWritten", run: testNothingWritten},
		{name: "RegisterFuncs", run: testRegisterFuncs},
		{name: "Lifecycle", run: testLifecycle},
		{name: "Reload", run: testReload},
		{name: "Concurrent", run: testConcurrent},
	}

	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			r.run(t, subject)
		})
	}
}

// service returns an initialized service over a store of subject holding
// seed, without static files, closed when the test ends.
func service(t *testing.T, subject Subject, seed fstest.MapFS) *ui.Service {
	t.Helper()

	cfg := ui.DefaultConfig()

	cfg.UI.StaticDir = ""

	s, err := ui.New[gas.TemplateProvider](
		ui.WithConfig(cfg),
	)(
		subject.New(t, seed),
		nil,
		nil,
		&gas.NopLogger{},
	)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	return s
}

// do renders name with Render, or with RenderFragment when frag is true,
// and returns the response and the error.
func do(
	t *testing.T,
	s ui.Provider,
	frag bool,
	name string,
	data any,
	opts ...ui.RenderOption,
) (*httptest.ResponseRecorder, error) {
	t.Helper()

	w := httptest.NewRecorder()

	var err error

	if frag {
		err = s.RenderFragment(t.Context(), w, name, data, opts...)
	} else {
		err = s.Render(t.Context(), w, name, data, opts...)
	}

	return w, err //nolint:wrapcheck // the rules check the error as the service returns it.
}

// ok renders as do does, requires no error, and returns the body.
func ok(
	t *testing.T,
	s ui.Provider,
	frag bool,
	name string,
	data any,
	opts ...ui.RenderOption,
) string {
	t.Helper()

	w, err := do(t, s, frag, name, data, opts...)
	require.NoError(t, err)

	return w.Body.String()
}

// testLayout: Render executes the layout with the page's blocks, escapes
// data, and sends HTML with 200; pages in directories are named by their
// path without the extension.
func testLayout(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	w, err := do(t, s, false, "home", "<b>")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "<html><h1>&lt;b&gt;</h1><nav>&lt;b&gt;</nav></html>", w.Body.String())

	assert.Equal(t, "<html><ul>u</ul><nav>u</nav></html>", ok(t, s, false, "admin/users", "u"))
	assert.Equal(t, "<html>TOP p<nav>p</nav></html>", ok(t, s, false, "plain", "p"),
		"a page without the block has its top-level text as the block, as RenderFragment shows")
	assert.Equal(t, "TOP p", ok(t, s, true, "plain", "p"))
	assert.Equal(t, "<html>default<nav>e</nav></html>", ok(t, s, false, "empty", "e"),
		"a page with neither gets the layout's own")
}

// testFragment: RenderFragment executes the page's content block (KI-032),
// else its top-level text, never a partial's stray text, else
// ErrNoFragment.
func testFragment(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	assert.Equal(t, "<h1>x</h1>", ok(t, s, true, "home", "x"))
	assert.Equal(t, "TOP x", ok(t, s, true, "plain", "x"))

	_, err := do(t, s, true, "empty", nil)
	require.ErrorIs(t, err, ui.ErrNoFragment)
	require.EqualError(
		t,
		err,
		`gas/ui: render fragment "empty": no fragment: the page defines no "content" block `+
			`and has no top-level text`,
	)
}

// testNoLayouts: with no layout in the store, Render executes the page's
// top-level text; WithLayout of a name the set lacks is ErrLayoutNotFound.
func testNoLayouts(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, fstest.MapFS{
		"page.html": {Data: []byte(`<p>{{.}}</p>{{define "content"}}c{{end}}`)},
	})

	assert.Equal(t, "<p>x</p>", ok(t, s, false, "page", "x"))
	assert.Equal(t, "c", ok(t, s, true, "page", "x"))

	_, err := do(t, s, false, "page", nil, ui.WithLayout("base"))
	require.ErrorIs(t, err, ui.ErrLayoutNotFound)
	require.EqualError(t, err, `gas/ui: render "page": layout not found: "base"`)
}

// testWithBlock: WithBlock executes any template of the page's set, many
// times and concurrently; a name the set lacks is ErrBlockNotFound.
func testWithBlock(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			assert.Equal(t, "<aside>A</aside>", ok(t, s, true, "home", "a", ui.WithBlock("aside")))
			assert.Equal(t, "<nav>n</nav>", ok(t, s, true, "home", "n", ui.WithBlock("nav")))
			assert.Equal(t, "<h1>c</h1>", ok(t, s, true, "home", "c", ui.WithBlock("content")))
		})
	}

	wg.Wait()

	_, err := do(t, s, true, "home", nil, ui.WithBlock("nope"))
	require.ErrorIs(t, err, ui.ErrBlockNotFound)
	require.EqualError(t, err, `gas/ui: render fragment "home": block not found: "nope"`)
}

// testNames: a name is a page's path without its extension; store names,
// layouts, partials, other extensions and unclean paths are not pages.
func testNames(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	for _, name := range []string{"home.html", "layouts/base", "partials/nav", "mail/welcome", "Home", "nope"} {
		_, err := do(t, s, false, name, nil)
		require.ErrorIs(t, err, ui.ErrPageNotFound, name)
		require.EqualError(t, err, `gas/ui: render "`+name+`": page not found`)
	}

	for name, reason := range map[string]string{
		"":        "empty",
		"/home":   unclean,
		"../home": unclean,
		"a//b":    unclean,
		"./home":  unclean,
		"a\\b":    "holds a backslash",
		"a\x00b":  "holds a NUL byte",
		"\xff":    "not valid UTF-8",
	} {
		_, err := do(t, s, false, name, nil)
		require.ErrorIs(t, err, ui.ErrInvalidName, name)
		require.EqualError(t, err, "gas/ui: render: invalid name: "+reason, name)
	}
}

// testIsolation: a page that fails to compile fails its own renders only
// (KI-053), at the first build and after, and does not fail CheckReady.
func testIsolation(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	_, err := do(t, s, false, "bad", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render "bad": compile failed: template: bad.html:1: bad character U+007D '}'`,
	)

	assert.Equal(t, "<h1>x</h1>", ok(t, s, true, "home", "x"))

	err = s.Reload(t.Context())
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: reload: compile failed: template: bad.html:1: bad character U+007D '}'`,
	)
	require.NoError(t, s.CheckReady(t.Context()))
	assert.Equal(t, "<h1>y</h1>", ok(t, s, true, "home", "y"))
}

// testReservedNames: a template that defines a name ending in an
// extension, such as another page's, fails to compile, so no page's root
// can be replaced.
func testReservedNames(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, fstest.MapFS{
		"a.html":      {Data: []byte(`A`)},
		"hijack.html": {Data: []byte(`{{define "a.html"}}hijack{{end}}`)},
	})

	assert.Equal(t, "A", ok(t, s, true, "a", nil))

	_, err := do(t, s, true, "hijack", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render fragment "hijack": compile failed: hijack.html defines "a.html", `+
			`a name ending in an extension, which only files have`,
	)
}

// testWrites: an execute error writes nothing; WithStatus writes the
// status; without it no status is written, so a handler's own stands.
func testWrites(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, fstest.MapFS{
		pageFile: {Data: []byte(`{{if .}}{{.}}{{else}}{{len 3}}{{end}}`)},
	})

	w := &spy{header: http.Header{}}
	err := s.Render(t.Context(), w, "p", nil)
	require.ErrorIs(t, err, ui.ErrExecute)
	require.EqualError(t, err, `gas/ui: render "p": execute failed: `+execText)

	var execErr texttemplate.ExecError

	require.ErrorAs(t, err, &execErr)
	w.empty(t, "execute error")

	data := "b"

	rec, err := do(t, s, true, "p", data, ui.WithStatus(http.StatusUnprocessableEntity))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "b", rec.Body.String())

	rec = httptest.NewRecorder()
	rec.WriteHeader(http.StatusConflict)
	require.NoError(t, s.RenderFragment(t.Context(), rec, "p", data))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// execText is html/template's error of the page of testWrites executed
// with nil.
const execText = `template: p.html:1:23: executing "p.html" at <len 3>: error calling len: len of type int`

// spy is an http.ResponseWriter that records what is written to it.
type spy struct {
	// header is the header map.
	header http.Header
	// body holds the bytes written.
	body bytes.Buffer
	// status is the status written, or 0.
	status int
}

// Header returns the header map.
func (w *spy) Header() http.Header { return w.header }

// Write records b.
func (w *spy) Write(b []byte) (int, error) {
	return w.body.Write(b) //nolint:wrapcheck // a bytes.Buffer's own error.
}

// WriteHeader records status.
func (w *spy) WriteHeader(status int) { w.status = status }

// empty requires that nothing was written to w: no header, no status, no
// byte.
func (w *spy) empty(t *testing.T, msg string) {
	t.Helper()

	assert.Empty(t, w.header, msg)
	assert.Zero(t, w.status, msg)
	assert.Zero(t, w.body.Len(), msg)
}

// testNothingWritten: a render that fails before it executes writes
// nothing: a page not found, a status refused, a block not found.
func testNothingWritten(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	cases := []struct {
		target error
		render func(http.ResponseWriter) error
		text   string
	}{
		{
			ui.ErrPageNotFound,
			func(w http.ResponseWriter) error { return s.Render(t.Context(), w, "nope", nil) },
			`gas/ui: render "nope": page not found`,
		},
		{
			ui.ErrInvalidStatus,
			func(w http.ResponseWriter) error {
				return s.Render(t.Context(), w, "home", nil, ui.WithStatus(http.StatusNoContent))
			},
			`gas/ui: render "home": invalid status: must be 200 to 599, and not 204 or 304`,
		},
		{
			ui.ErrBlockNotFound,
			func(w http.ResponseWriter) error {
				return s.RenderFragment(t.Context(), w, "home", nil, ui.WithBlock("nope"))
			},
			`gas/ui: render fragment "home": block not found: "nope"`,
		},
	}

	for _, c := range cases {
		w := &spy{header: http.Header{}}
		err := c.render(w)
		require.ErrorIs(t, err, c.target)
		require.EqualError(t, err, c.text)
		w.empty(t, c.text)
	}
}

// testRegisterFuncs: RegisterFuncs checks every function first and adds
// none when one fails.
func testRegisterFuncs(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, fstest.MapFS{pageFile: {Data: []byte(`{{good}}`)}})
	good := func() string { return "G" }

	err := s.RegisterFuncs(template.FuncMap{goodFunc: good, "html": func() string { return "" }})
	require.ErrorIs(t, err, ui.ErrInvalidFunc)
	require.EqualError(
		t,
		err,
		`gas/ui: register funcs: invalid func: "html": reserved by html/template`,
	)

	err = s.RegisterFuncs(template.FuncMap{goodFunc: good, "zbad": nil})
	require.ErrorIs(t, err, ui.ErrInvalidFunc)
	require.EqualError(t, err, `gas/ui: register funcs: invalid func: "zbad": nil`)

	_, err = do(t, s, false, "p", nil)
	require.ErrorIs(t, err, ui.ErrCompile)
	require.EqualError(
		t,
		err,
		`gas/ui: render "p": compile failed: template: p.html:1: function "good" not defined`,
	)

	require.NoError(t, s.RegisterFuncs(template.FuncMap{goodFunc: good}))
	assert.Equal(t, "G", ok(t, s, false, "p", nil))
}

// order is the seed of testEscapeOrder: a layout that closes an attribute
// a partial opens, so that the partial escapes only inside the layout.
var order = fstest.MapFS{
	layoutFile: {
		Data: []byte(
			`{{define "base"}}{{template "open" .}}">link</a>{{block "content" .}}{{end}}{{end}}`,
		),
	},
	"partials/open.html": {Data: []byte(`{{define "open"}}<a href="/u/{{.}}{{end}}`)},
	pageFile:             {Data: []byte(`{{define "content"}}<p>{{.}}</p>{{end}}`)},
}

// testEscapeOrder: what a render returns never depends on what ran
// before it. Render then WithBlock of the partial, and the other way
// round, give the same results, and never panic, where WithBlock first
// once left the partial without a body and every later Render panicked.
func testEscapeOrder(t *testing.T, subject Subject) {
	t.Helper()

	type result struct {
		body string
		err  string
	}

	run := func(s *ui.Service, frag bool) result {
		var (
			w   *httptest.ResponseRecorder
			err error
		)

		require.NotPanics(t, func() {
			if frag {
				w, err = do(t, s, true, "p", "x", ui.WithBlock("open"))
			} else {
				w, err = do(t, s, false, "p", "x")
			}
		})

		if err != nil {
			return result{body: w.Body.String(), err: err.Error()}
		}

		return result{body: w.Body.String()}
	}

	renderFirst := service(t, subject, order)
	blockFirst := service(t, subject, order)

	var a, b [3]result

	a[0], a[1], a[2] = run(renderFirst, false), run(renderFirst, true), run(renderFirst, false)
	b[1], b[0], b[2] = run(blockFirst, true), run(blockFirst, false), run(blockFirst, false)

	assert.Equal(t, a, b)
	assert.Equal(t, result{body: `<a href="/u/x">link</a><p>x</p>`}, a[0])
	assert.Equal(t, a[0], a[2])
	assert.Empty(t, a[1].body)

	_, err := do(t, blockFirst, true, "p", "x", ui.WithBlock("open"))
	require.ErrorIs(t, err, ui.ErrExecute)
	assert.True(t, strings.HasPrefix(err.Error(), `gas/ui: render fragment "p": execute failed: `+
		`html/template:open: ends in a non-text context`), err.Error())
	assert.Equal(t, a[1].err, err.Error())

	var escErr *template.Error

	require.ErrorAs(t, err, &escErr)
	assert.Equal(t, template.ErrEndContext, escErr.ErrorCode)
}

// testEscapeAlone: each template a render executes is escaped alone, as
// a new html/template set of the same files executing only it would, in
// any order: a block the layout calls in an attribute, a script and a
// style escapes for text when WithBlock executes it.
func testEscapeAlone(t *testing.T, subject Subject) {
	t.Helper()

	layout := `{{define "base"}}<a href="/x?q={{block "u" .}}{{.}}{{end}}" title="{{template "t" .}}">` +
		`{{block "content" .}}{{end}}</a><script>var x = {{template "t" .}};</script>` +
		`<style>p{color:{{template "t" .}}}</style>{{end}}`
	partial := `{{define "t"}}{{.}}{{end}}`
	page := `{{define "content"}}<b>{{.}}</b>{{template "t" .}}{{end}}{{define "u"}}{{.}}&z={{.}}{{end}}`
	data := `"><script>alert(1)</script>'&`
	seed := fstest.MapFS{
		layoutFile:        {Data: []byte(layout)},
		"partials/t.html": {Data: []byte(partial)},
		pageFile:          {Data: []byte(page)},
	}

	want := func(entry string) string {
		ref := template.New("ref")

		for _, src := range []string{layout, partial, page} {
			_, err := ref.Parse(src)
			require.NoError(t, err)
		}

		var b bytes.Buffer

		require.NoError(t, ref.ExecuteTemplate(&b, entry, data))

		return b.String()
	}

	for _, steps := range [][]string{
		{layoutName, "u", "t", contentBlock},
		{"u", "t", contentBlock, layoutName},
		{"t", layoutName, contentBlock, "u"},
	} {
		s := service(t, subject, seed)

		for _, step := range steps {
			if step == layoutName {
				assert.Equal(t, want(step), ok(t, s, false, "p", data), "steps %v", steps)

				continue
			}

			assert.Equal(
				t,
				want(step),
				ok(t, s, true, "p", data, ui.WithBlock(step)),
				"steps %v, step %s",
				steps,
				step,
			)
		}
	}
}

// testLifecycle: the sentinels before Init, after Close and on a second
// Init; Close then Init restarts, and registered functions survive it.
func testLifecycle(t *testing.T, subject Subject) {
	t.Helper()

	ctx := t.Context()
	cfg := ui.DefaultConfig()

	cfg.UI.StaticDir = ""

	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(subject.New(t, fstest.MapFS{
		pageFile: {Data: []byte(`{{shout .}}`)},
	}), nil, nil, &gas.NopLogger{})
	require.NoError(t, err)

	_, err = do(t, s, false, "p", "x")
	require.ErrorIs(t, err, ui.ErrNotInitialized)
	require.EqualError(t, err, `gas/ui: render "p": service not initialized`)

	err = s.RegisterFuncs(nil)
	require.ErrorIs(t, err, ui.ErrNotInitialized)
	require.EqualError(t, err, "gas/ui: register funcs: service not initialized")

	require.NoError(t, s.Init(ctx))

	err = s.Init(ctx)
	require.ErrorIs(t, err, ui.ErrAlreadyInitialized)
	require.EqualError(t, err, "gas/ui: init: service already initialized")
	require.NoError(
		t,
		s.RegisterFuncs(template.FuncMap{"shout": func(v string) string { return v + "!" }}),
	)
	assert.Equal(t, "x!", ok(t, s, false, "p", "x"))

	require.NoError(t, s.Close(ctx))

	w, err := do(t, s, false, "p", "x")
	require.ErrorIs(t, err, ui.ErrClosed)
	require.EqualError(t, err, `gas/ui: render "p": service closed`)
	assert.Equal(t, 0, w.Body.Len())

	err = s.CheckReady(ctx)
	require.ErrorIs(t, err, ui.ErrClosed)
	require.EqualError(t, err, "gas/ui: ready: service closed")

	require.NoError(t, s.Init(ctx))
	assert.Equal(t, "y!", ok(t, s, false, "p", "y"))
	require.NoError(t, s.CheckReady(ctx))
	require.NoError(t, s.Close(ctx))
}

// listing is a gas.TemplateProvider that counts the calls of List.
type listing struct {
	gas.TemplateProvider

	// lists counts the calls of List.
	lists atomic.Int64
}

// List counts the call and returns the store's names.
func (l *listing) List(ctx context.Context) ([]string, error) {
	l.lists.Add(1)

	return l.TemplateProvider.List(ctx) //nolint:wrapcheck // the store's own error.
}

// testReload: Reload reads the store again on every store; outside
// reload mode a store change is seen on Reload only.
func testReload(t *testing.T, subject Subject) {
	t.Helper()

	ctx := t.Context()
	cfg := ui.DefaultConfig()

	store := &listing{
		TemplateProvider: subject.New(t, fstest.MapFS{"a.html": {Data: []byte(`old`)}}),
	}

	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(store, nil, nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	assert.Equal(t, "old", ok(t, s, false, "a", nil))
	assert.Equal(t, "old", ok(t, s, false, "a", nil))
	assert.Equal(t, int64(1), store.lists.Load(), "renders build once")

	listed := store.lists.Load()

	require.NoError(t, s.Reload(ctx))
	assert.Equal(t, listed+1, store.lists.Load(), "Reload reads the store again")
	assert.Equal(t, "old", ok(t, s, false, "a", nil))

	err = store.Register(ctx, "b.html", []byte(`new`))
	if errors.Is(err, tmpl.ErrReadOnly) {
		return
	}

	require.NoError(t, err)

	_, err = do(t, s, false, "b", nil)
	require.ErrorIs(t, err, ui.ErrPageNotFound)
	require.EqualError(t, err, `gas/ui: render "b": page not found`)

	require.NoError(t, s.Reload(ctx))
	assert.Equal(t, "new", ok(t, s, false, "b", nil))
}

// testConcurrent: renders, fragments, function registrations, reloads
// and readiness checks from many goroutines, for -race.
func testConcurrent(t *testing.T, subject Subject) {
	t.Helper()

	s := service(t, subject, site)

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				_, _ = do(t, s, false, "home", j)
				_, _ = do(t, s, true, "plain", j, ui.WithBlock("aside"))
				_ = s.CheckReady(t.Context())
			}
		})
		wg.Go(func() {
			name := "f" + strconv.Itoa(i)

			err := s.RegisterFuncs(template.FuncMap{name: func() string { return name }})
			if err != nil {
				t.Errorf("register funcs: %v", err)
			}

			_ = s.Reload(t.Context())
		})
	}

	wg.Wait()

	assert.Equal(t, "<h1>z</h1>", ok(t, s, true, "home", "z"))
}

// RunMockDefaults runs the rules of the defaults of the mock of
// gas/ui/uitest on the Provider newMock returns with every field nil:
// Render and RenderFragment write nothing and return nil for any name,
// data and options, nil and invalid ones included, and RegisterFuncs
// accepts every map, nil and invalid functions included.
func RunMockDefaults(t *testing.T, newMock func() ui.Provider) {
	t.Helper()

	t.Run("Renders", func(t *testing.T) {
		t.Parallel()

		m := newMock()

		for _, name := range []string{"", "home", "../x", "a\\b", "\xff", "a\x00"} {
			for _, opts := range [][]ui.RenderOption{
				nil,
				{nil},
				{ui.WithStatus(-1)},
				{ui.WithStatus(http.StatusNoContent)},
				{ui.WithBlock("x"), ui.WithLayout("y")},
			} {
				w := &spy{header: http.Header{}}
				require.NoError(t, m.Render(t.Context(), w, name, "data", opts...))
				w.empty(t, "Render "+name)

				w = &spy{header: http.Header{}}
				require.NoError(t, m.RenderFragment(t.Context(), w, name, "data", opts...))
				w.empty(t, "RenderFragment "+name)
			}
		}
	})

	t.Run("RegisterFuncs", func(t *testing.T) {
		t.Parallel()

		m := newMock()

		for _, funcs := range []template.FuncMap{
			nil,
			{},
			{"ok": func() string { return "" }},
			{"bad-name": func() string { return "" }},
			{"f": nil},
			{"f": 1},
			{"f": func() {}},
			{"html": func(s string) string { return s }},
		} {
			require.NoError(t, m.RegisterFuncs(funcs))
		}
	})
}
