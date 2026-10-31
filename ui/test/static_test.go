package ui_test

import (
	"context"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

// staticTree writes a static directory with assorted files next to a
// directory holding a secret, and returns the static directory.
func staticTree(tb testing.TB) string {
	tb.Helper()

	root := tb.TempDir()
	static := filepath.Join(root, "static")
	outside := filepath.Join(root, "outside")

	for _, d := range []string{
		static, outside, filepath.Join(static, "sub"), filepath.Join(static, ".git"),
		filepath.Join(static, ".well-known"),
	} {
		require.NoError(tb, os.MkdirAll(d, 0o750))
	}

	for name, content := range map[string]string{
		"outside/secret.txt":       "TOPSECRET",
		"static/app.css":           "body{}",
		"static/.env":              "DB_PASSWORD=hunter2",
		"static/.git/config":       "[core]",
		"static/.well-known/x.txt": "wk",
		"static/sub/index.html":    "<h1>idx</h1>",
		"static/noext":             "<html><script>alert(1)</script>",
		"static/big.bin":           strings.Repeat("a", 1000),
	} {
		require.NoError(
			tb,
			os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o600),
		)
	}

	require.NoError(
		tb,
		os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(static, "abs.txt")),
	)
	require.NoError(tb, os.Symlink("../outside/secret.txt", filepath.Join(static, "rel.txt")))
	require.NoError(tb, os.Symlink("../outside", filepath.Join(static, "linkdir")))
	require.NoError(tb, os.Symlink("app.css", filepath.Join(static, "inside.css")))

	return static
}

// rootHandler returns a static handler over the os.Root of dir, which is
// closed when the test ends.
func rootHandler(t *testing.T, prefix, dir string, opts ...ui.StaticOption) http.Handler {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	h, err := ui.StaticHandler(prefix, root.FS(), opts...)
	require.NoError(t, err)

	return h
}

// serve sends a request of method for the raw URL path target to h, with
// the headers hdr.
func serve(
	h http.Handler,
	method, target string,
	hdr map[string]string,
) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, "/", nil)

	r.URL.Path = target

	for k, v := range hdr {
		r.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	return w
}

// A static directory served through os.Root serves its files, the
// symlinks inside it included, and answers 404 for every path that
// leaves it, by `..` or by a symlink, absolute or relative, where
// http.Dir once followed symlinks out.
func TestStatic_Root(t *testing.T) {
	t.Parallel()

	h := rootHandler(t, "/static/", staticTree(t))

	for path, want := range map[string]string{
		"/static/app.css":        "body{}",
		"/static/inside.css":     "body{}",
		"/static/sub/index.html": "<h1>idx</h1>",
	} {
		w := serve(h, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusOK, w.Code, path)
		assert.Equal(t, want, w.Body.String(), path)
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), path)
	}

	for _, path := range []string{
		"/static/abs.txt", "/static/rel.txt", "/static/linkdir/secret.txt", "/static/../outside/secret.txt",
		"/static/nope.css", "/static/app.css/x", "/static/sub", "/static/sub/", "/static/", "/static",
		"/other/app.css", "/staticapp.css", "/static/app.css/", "/static/a\\b", "/static/a\x00b",
		"/static/\xff", "/static/" + strings.Repeat("a", 5000),
	} {
		w := serve(h, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.NotContains(t, w.Body.String(), "TOPSECRET", path)
		assert.Empty(t, w.Header().Get("Location"), path)
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), path)
	}
}

// A path that is not clean, with a `.` or `..` element or an empty one,
// is not found, also when it came percent-encoded, where it was once
// cleaned, so that `..` walked out of the route's scope: through the
// route `/css/*` with no prefix removed, `/css/../private/secret.txt`
// served a file outside `css/`.
func TestStatic_UncleanPaths(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "css"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "css", "site.css"), []byte("css{}"), 0o600))

	h := rootHandler(t, "/static/", dir)

	for _, path := range []string{
		"/static/sub/../app.css", "/static//app.css", "/static/./app.css", "/static/app.css/.",
		"/static/sub/.", "/static/sub//index.html", "/static/sub/./index.html", "/static/..",
	} {
		assert.Equal(t, http.StatusNotFound, serve(h, http.MethodGet, path, nil).Code, path)
	}

	router := gas.NewRouter()

	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfgWith(func(set *ui.Settings) {
		set.StaticDir, set.StaticPath, set.StaticPaths = dir, "", []string{"/css/*"}
	})))(newStore(t, nil), router, nil, newRecorder())
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	router.Seal()

	for target, code := range map[string]int{
		"/css/site.css":                  http.StatusOK,
		"/css/../app.css":                http.StatusNotFound,
		"/css/%2e%2e/app.css":            http.StatusNotFound,
		"/css/..%2fapp.css":              http.StatusNotFound,
		"/css/.%2fsite.css":              http.StatusNotFound,
		"/css/%2fsite.css":               http.StatusNotFound,
		"/css/%2e%2e%2f%2e%2e%2fapp.css": http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(
			w,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil),
		)
		assert.Equal(t, code, w.Code, target)
		assert.NotEqual(t, "body{}", w.Body.String(), target)
	}
}

// os.Root refuses a symlink that leaves it with an error that os does not
// export; the handler matches its text, which this pins.
func TestStatic_RootEscapeError(t *testing.T) {
	t.Parallel()

	root, err := os.OpenRoot(staticTree(t))
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, err = fs.Stat(root.FS(), "rel.txt")

	var pe *fs.PathError

	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "path escapes from parent", pe.Err.Error())
}

// Paths with an element that starts with `.` are not found unless
// WithDotfiles allows them.
func TestStatic_Dotfiles(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)
	h := rootHandler(t, "/static/", dir)

	for _, path := range []string{"/static/.env", "/static/.git/config", "/static/.well-known/x.txt"} {
		w := serve(h, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.NotContains(t, w.Body.String(), "hunter2")
	}

	h = rootHandler(t, "/static/", dir, ui.WithDotfiles(true))
	assert.Equal(t, "wk", serve(h, http.MethodGet, "/static/.well-known/x.txt", nil).Body.String())
	assert.Equal(t, "[core]", serve(h, http.MethodGet, "/static/.git/config", nil).Body.String())
}

// The dotfile rule applies to the URL path, as documented: a symlink
// named without a dot that leads to a dotfile inside the root serves it.
func TestStatic_DotfileSymlink(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)
	require.NoError(t, os.Symlink(".env", filepath.Join(dir, "env.txt")))

	h := rootHandler(t, "/static/", dir)

	assert.Equal(t, http.StatusNotFound, serve(h, http.MethodGet, "/static/.env", nil).Code)
	assert.Equal(
		t,
		"DB_PASSWORD=hunter2",
		serve(h, http.MethodGet, "/static/env.txt", nil).Body.String(),
	)
}

// A file the process may not read answers 404 with the body of a missing
// one, so that a response never tells the two apart.
func TestStatic_UnreadableFile(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("L"), 0o000))

	h := rootHandler(t, "/static/", dir)

	locked := serve(h, http.MethodGet, "/static/locked.txt", nil)
	missing := serve(h, http.MethodGet, "/static/missing.txt", nil)

	assert.Equal(t, http.StatusNotFound, locked.Code)
	assert.Equal(t, missing.Code, locked.Code)
	assert.Equal(t, missing.Body.String(), locked.Body.String())
}

// Through the service's route, a method other than GET and HEAD gets the
// router's 405, as documented: an empty body and one Allow field per
// method of the route.
func TestStatic_RouterMethodNotAllowed(t *testing.T) {
	t.Parallel()

	router := gas.NewRouter()

	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfgWith(func(set *ui.Settings) {
		set.StaticDir = staticTree(t)
	})))(newStore(t, nil), router, nil, newRecorder())
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	router.Seal()

	for _, m := range []string{http.MethodPost, http.MethodOptions} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), m, "/static/app.css", nil))
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, m)
		assert.JSONEq(
			t,
			`{"error":{"status":405,"code":"method_not_allowed","message":"method not allowed"}}`,
			w.Body.String(),
			m,
		)
		assert.ElementsMatch(
			t,
			[]string{http.MethodGet, http.MethodHead},
			w.Header().Values("Allow"),
			m,
		)
	}
}

// The handler serves GET and HEAD only, with `Allow` on 405.
func TestStatic_Methods(t *testing.T) {
	t.Parallel()

	h := rootHandler(t, "/static/", staticTree(t))

	w := serve(h, http.MethodHead, "/static/app.css", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "6", w.Header().Get("Content-Length"))

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions, "BREW"} {
		w = serve(h, m, "/static/app.css", nil)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, m)
		assert.Equal(t, "GET, HEAD", w.Header().Get("Allow"), m)
	}
}

// Every file is served with its type, by extension or else sniffed, and
// with nosniff, so a browser never runs a file as another type; HTML is
// served as HTML.
func TestStatic_ContentTypes(t *testing.T) {
	t.Parallel()

	h := rootHandler(t, "/static/", staticTree(t))

	for path, want := range map[string]string{
		"/static/app.css":        "text/css; charset=utf-8",
		"/static/sub/index.html": "text/html; charset=utf-8",
		"/static/noext":          "text/html; charset=utf-8",
	} {
		w := serve(h, http.MethodGet, path, nil)
		assert.Equal(t, want, w.Header().Get("Content-Type"), path)
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), path)
	}
}

// From a directory, a file has Last-Modified and answers conditional and
// range requests; WithMaxAge adds Cache-Control.
func TestStatic_DirCaching(t *testing.T) {
	t.Parallel()

	h := rootHandler(
		t,
		"/static/",
		staticTree(t),
		ui.WithMaxAge(90*time.Minute+500*time.Millisecond),
	)

	w := serve(h, http.MethodGet, "/static/app.css", nil)
	lm := w.Header().Get("Last-Modified")
	assert.NotEmpty(t, lm)
	assert.Empty(t, w.Header().Get("ETag"))
	assert.Equal(t, "public, max-age=5400", w.Header().Get("Cache-Control"))

	w = serve(h, http.MethodGet, "/static/app.css", map[string]string{"If-Modified-Since": lm})
	assert.Equal(t, http.StatusNotModified, w.Code)

	w = serve(h, http.MethodGet, "/static/big.bin", map[string]string{"Range": "bytes=0-9"})
	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "aaaaaaaaaa", w.Body.String())

	w = serve(h, http.MethodGet, "/static/nope", nil)
	assert.Empty(t, w.Header().Get("Cache-Control"))
}

// From an fs.FS without mod times, such as an embed.FS, a file gets an
// ETag, a hash of its content kept per handler, so If-None-Match answers
// 304; a file of a new size gets a new tag.
func TestStatic_FSETag(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"app.css": {Data: []byte("body{}")}, "app.js": {Data: []byte("x()")}}

	h, err := ui.StaticHandler("/static/", fsys)
	require.NoError(t, err)

	w := serve(h, http.MethodGet, "/static/app.css", nil)
	tag := w.Header().Get("ETag")
	assert.Regexp(t, `^"[0-9a-f]{32}"$`, tag)
	assert.Empty(t, w.Header().Get("Last-Modified"))
	assert.Equal(t, tag, serve(h, http.MethodGet, "/static/app.css", nil).Header().Get("ETag"))
	assert.NotEqual(t, tag, serve(h, http.MethodGet, "/static/app.js", nil).Header().Get("ETag"))

	w = serve(h, http.MethodGet, "/static/app.css", map[string]string{"If-None-Match": tag})
	assert.Equal(t, http.StatusNotModified, w.Code)

	fsys["app.css"] = &fstest.MapFile{Data: []byte("body{color:red}")}

	assert.NotEqual(t, tag, serve(h, http.MethodGet, "/static/app.css", nil).Header().Get("ETag"))
	assert.Equal(
		t,
		"body{color:red}",
		serve(h, http.MethodGet, "/static/app.css", nil).Body.String(),
	)
}

// The handler keeps the ETags of at most 4096 files, and serves a file
// past them without one, where it once hashed the file on every request:
// a HEAD with a Range of a large file read all of it each time.
func TestStatic_ETagBound(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{}
	for i := range 4100 {
		fsys["f"+strconv.Itoa(i)] = &fstest.MapFile{Data: []byte(strconv.Itoa(i))}
	}

	h, err := ui.StaticHandler("", fsys)
	require.NoError(t, err)

	tagged := map[string]string{}

	for _, name := range slices.Sorted(maps.Keys(fsys)) {
		w := serve(h, http.MethodGet, "/"+name, nil)
		require.Equal(t, http.StatusOK, w.Code)

		if tag := w.Header().Get("ETag"); tag != "" {
			tagged[name] = tag
		}
	}

	require.Len(t, tagged, 4096)

	for name := range fsys {
		w := serve(h, http.MethodGet, "/"+name, nil)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, tagged[name], w.Header().Get("ETag"), name)
	}
}

// A HEAD or a range request never hashes a file whose tag is not kept: it
// is served without an ETag, and a GET then computes and keeps it, which
// later HEAD and range requests send.
func TestStatic_ETagHeadAndRange(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"a.css": {Data: []byte("body{}")}, "b.css": {Data: []byte("p{}")}}

	h, err := ui.StaticHandler("/s/", fsys)
	require.NoError(t, err)

	w := serve(h, http.MethodHead, "/s/a.css", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Header().Get("ETag"))

	w = serve(h, http.MethodGet, "/s/b.css", map[string]string{"Range": "bytes=0-0"})
	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Empty(t, w.Header().Get("ETag"))

	tag := serve(h, http.MethodGet, "/s/a.css", nil).Header().Get("ETag")
	require.NotEmpty(t, tag)
	assert.Equal(t, tag, serve(h, http.MethodHead, "/s/a.css", nil).Header().Get("ETag"))
	assert.Equal(
		t,
		tag,
		serve(
			h,
			http.MethodGet,
			"/s/a.css",
			map[string]string{"Range": "bytes=0-0"},
		).Header().
			Get("ETag"),
	)
}

// An empty prefix removes nothing: the URL path is the file's path, and
// an empty path or the root lists nothing, where it once listed the
// directory.
func TestStatic_EmptyPrefix(t *testing.T) {
	t.Parallel()

	h := rootHandler(t, "", staticTree(t))

	assert.Equal(t, "body{}", serve(h, http.MethodGet, "/app.css", nil).Body.String())

	for _, path := range []string{"", "/", "/sub", "/sub/"} {
		w := serve(h, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.NotContains(t, w.Body.String(), "<a href", path)
	}
}

// StaticHandler refuses a prefix that is not empty and does not start and
// end with `/`, a nil fs.FS, a nil option and a max age under a second.
func TestStatic_HandlerErrors(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{}

	for _, prefix := range []string{"/static", "static/", "static", "/"} {
		_, err := ui.StaticHandler(prefix, fsys)
		if prefix == "/" {
			require.NoError(t, err)

			continue
		}

		require.ErrorIs(t, err, ui.ErrInvalidPrefix, prefix)
		require.EqualError(
			t,
			err,
			"gas/ui: static handler: invalid prefix: must be empty or start and end with `/`",
		)
	}

	_, err := ui.StaticHandler("/s/", nil)
	require.ErrorIs(t, err, ui.ErrNilFS)
	require.EqualError(t, err, "gas/ui: static handler: file system is nil")

	_, err = ui.StaticHandler("/s/", fsys, nil)
	require.EqualError(t, err, "gas/ui: static handler: invalid option: nil")

	for _, d := range []time.Duration{-time.Second, time.Millisecond, 999 * time.Millisecond} {
		_, err = ui.StaticHandler("/s/", fsys, ui.WithMaxAge(d))
		require.ErrorIs(t, err, ui.ErrInvalidOption)
		require.EqualError(
			t,
			err,
			"gas/ui: static handler: invalid option: max age must be 0 or at least 1s",
		)
	}

	_, err = ui.StaticHandler("/s/", fsys, ui.WithMaxAge(0), ui.WithMaxAge(time.Second))
	require.NoError(t, err)
}

// errFS is an fs.FS whose Stat succeeds and whose Open fails with err, or
// returns a file that is not seekable when err is nil.
type errFS struct {
	// err is what Open returns.
	err error
}

// Open fails with fsys.err, or returns an unseekable file.
func (fsys errFS) Open(name string) (fs.File, error) {
	if fsys.err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fsys.err}
	}

	return plainFile{}, nil
}

// Stat describes a regular file.
func (errFS) Stat(string) (fs.FileInfo, error) { return plainFile{}.Stat() }

// plainFile is an fs.File that reads nothing and cannot seek.
type plainFile struct{}

// Stat describes a regular file.
func (plainFile) Stat() (fs.FileInfo, error) {
	return fstest.MapFS{"f": {Data: []byte("x")}}.Stat("f")
}

// Read reads nothing.
func (plainFile) Read([]byte) (int, error) { return 0, io.EOF }

// Close does nothing.
func (plainFile) Close() error { return nil }

// An error other than a missing or unreachable file answers 500 and
// reaches WithErrorFunc; permission answers 404, as for a missing file,
// where it was once 403 and told a file that exists from one that does
// not; a closed root 503; a file that cannot seek 500.
func TestStatic_Errors(t *testing.T) {
	t.Parallel()

	var got []error

	onError := ui.WithErrorFunc(func(_ *http.Request, err error) { got = append(got, err) })
	broken := errDisk

	for err, want := range map[error]int{
		broken:           http.StatusInternalServerError,
		fs.ErrPermission: http.StatusNotFound,
		os.ErrClosed:     http.StatusServiceUnavailable,
		fs.ErrNotExist:   http.StatusNotFound,
		nil:              http.StatusInternalServerError,
	} {
		h, herr := ui.StaticHandler("/", errFS{err: err}, onError)
		require.NoError(t, herr)
		assert.Equal(t, want, serve(h, http.MethodGet, "/f", nil).Code, err)
	}

	require.Len(t, got, 2)

	h, err := ui.StaticHandler("/", errFS{err: broken})
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, serve(h, http.MethodGet, "/f", nil).Code)

	root, err := os.OpenRoot(staticTree(t))
	require.NoError(t, err)

	h, err = ui.StaticHandler("/", root.FS())
	require.NoError(t, err)
	require.NoError(t, root.Close())
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, http.MethodGet, "/app.css", nil).Code)
}

// The service opens StaticDir as an os.Root at Init: a missing directory
// or a file fails Init with an error that names the setting, not the
// path.
func TestStatic_ServiceDir(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)
	router := gas.NewRouter()

	for name, want := range map[string]string{
		filepath.Join(dir, "missing"): "gas/ui: init: invalid config: UI.StaticDir cannot be opened as a directory: " +
			"no such file or directory",
		filepath.Join(dir, "app.css"): "gas/ui: init: invalid config: UI.StaticDir cannot be opened as a directory: " +
			"not a directory",
	} {
		s, err := ui.New[gas.TemplateProvider](
			ui.WithConfig(cfgWith(func(set *ui.Settings) { set.StaticDir = name })),
		)(
			newStore(t, nil),
			router,
			nil,
			newRecorder(),
		)
		require.NoError(t, err)

		err = s.Init(t.Context())
		require.ErrorIs(t, err, ui.ErrInvalidConfig)
		require.EqualError(t, err, want)
		assert.NotContains(t, err.Error(), dir)
	}
}

// FuzzStaticPath checks, for any URL path, that the handler never
// panics, never serves the secret outside the root, a dotfile or a
// listing, answers only 200, 404 or 405, and serves a file only for a
// path that names it under the prefix.
func FuzzStaticPath(f *testing.F) {
	for _, seed := range []string{
		"/static/app.css", "", "/", "/static", "/static/", "/static/../outside/secret.txt", "/static/rel.txt",
		"/static/linkdir/secret.txt", "/static/.env", "/static/sub/", "/static/sub", "/static//app.css",
		"/static/sub/../../outside/secret.txt", "/static/%2e%2e/x", "/static/a\x00", "/static/.git/config",
		"/static/abs.txt", "/static/./.env",
	} {
		f.Add(http.MethodGet, seed)
	}

	f.Add(http.MethodPost, "/static/app.css")

	dir := staticTree(f)

	root, err := os.OpenRoot(dir)
	require.NoError(f, err)
	f.Cleanup(func() { require.NoError(f, root.Close()) })

	hs := map[string]http.Handler{}

	for _, prefix := range []string{"/static/", ""} {
		h, err := ui.StaticHandler(prefix, root.FS())
		require.NoError(f, err)

		hs[prefix] = h
	}

	f.Fuzz(func(t *testing.T, method, path string) {
		for prefix, h := range hs {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			r.Method = method
			r.URL.Path = path

			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			b := w.Body.String()
			assert.NotContains(t, b, "TOPSECRET")
			assert.NotContains(t, b, "hunter2")
			assert.NotContains(t, b, "[core]")
			assert.NotContains(t, b, "<a href")
			assert.Contains(
				t,
				[]int{http.StatusOK, http.StatusNotFound, http.StatusMethodNotAllowed},
				w.Code,
			)
			assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))

			if w.Code == http.StatusOK {
				assertServed(t, prefix, path, b)
			}
		}
	})
}

// assertServed checks a file FuzzStaticPath was served: its path is
// under prefix and clean, and its body is one of the tree's files.
func assertServed(t *testing.T, prefix, path, body string) {
	t.Helper()

	assert.True(t, strings.HasPrefix(path, prefix), path)

	rest := strings.TrimPrefix(path, prefix)
	if prefix == "" {
		rest = strings.TrimPrefix(rest, "/")
	}

	assert.Equal(t, "/"+rest, pathpkg.Clean("/"+rest), path)
	assert.Contains(t, []string{
		"body{}", "<h1>idx</h1>", "<html><script>alert(1)</script>",
		strings.Repeat("a", 1000), "",
	}, body)
}
