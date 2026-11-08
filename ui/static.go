package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// opStatic is the operation of StaticHandler's errors.
const opStatic = "static handler"

// maxETags is the most files whose ETag a static handler keeps.
const maxETags = 4096

// StaticOption configures a static handler.
type StaticOption func(*staticOptions)

// staticOptions are the options of a static handler.
type staticOptions struct {
	// onError receives the errors answered with 500, or is nil.
	onError func(*http.Request, error)
	// maxAge is the Cache-Control max-age, or 0 for no header.
	maxAge time.Duration
	// dotfiles serves names with an element that starts with `.`.
	dotfiles bool
}

// WithDotfiles makes the handler serve, when allow is true, files whose
// path holds an element that starts with `.`, such as `.well-known/x` or
// `.env`.
//
// Default: false; such a path is not found.
func WithDotfiles(allow bool) StaticOption {
	return func(o *staticOptions) { o.dotfiles = allow }
}

// WithMaxAge makes the handler send `Cache-Control: public, max-age=N`
// with every file, N being d in whole seconds. d must be 0 or at least a
// second, or StaticHandler fails with ErrInvalidOption; 0 sends no
// header.
//
// Default: 0.
func WithMaxAge(d time.Duration) StaticOption {
	return func(o *staticOptions) { o.maxAge = d }
}

// WithErrorFunc makes the handler call f with the request and the error
// of each file it answers with 500 Internal Server Error, such as a read
// that failed; f may log it. A nil f is no call.
//
// Default: no call.
func WithErrorFunc(f func(r *http.Request, err error)) StaticOption {
	return func(o *staticOptions) { o.onError = f }
}

// staticHandler serves the files of an fs.FS, as described in the package
// documentation.
type staticHandler struct {
	// fsys holds the files.
	fsys fs.FS
	// onError receives the errors answered with 500, or is nil.
	onError func(*http.Request, error)
	// etags holds the ETag of each file of fsys without a mod time.
	etags map[string]etag
	// prefix is removed from the URL path.
	prefix string
	// cacheControl is the Cache-Control header, or empty.
	cacheControl string
	// dotfiles serves names with an element that starts with `.`.
	dotfiles bool
	// mu guards etags.
	mu sync.Mutex
}

// etag is the ETag of a file of a given size.
type etag struct {
	// tag is the quoted ETag.
	tag string
	// size is the size of the file the tag was computed for.
	size int64
}

// StaticHandler returns a handler that serves the files of fsys under the
// URL path prefix, as described under Static handler in the package
// documentation. prefix is empty, to remove nothing, or starts and ends
// with `/`; anything else is ErrInvalidPrefix. A nil fsys is ErrNilFS.
// To serve a directory, pass the FS of an os.Root, which keeps every
// file, through symlinks too, inside it.
func StaticHandler(prefix string, fsys fs.FS, opts ...StaticOption) (http.Handler, error) {
	var o staticOptions

	for _, opt := range opts {
		if opt == nil {
			return nil, opError(opStatic, "", fmt.Errorf("%w: nil", ErrInvalidOption))
		}

		opt(&o)
	}

	h, err := newStaticHandler(prefix, fsys, &o)
	if err != nil {
		return nil, opError(opStatic, "", err)
	}

	return h, nil
}

// newStaticHandler returns the handler of StaticHandler, or the error it
// returns after the operation.
func newStaticHandler(prefix string, fsys fs.FS, o *staticOptions) (*staticHandler, error) {
	switch {
	case fsys == nil:
		return nil, ErrNilFS
	case prefix != "" && !validPrefix(prefix):
		return nil, fmt.Errorf("%w: must be empty or start and end with `/`", ErrInvalidPrefix)
	case o.maxAge != 0 && o.maxAge < time.Second:
		return nil, fmt.Errorf("%w: max age must be 0 or at least 1s", ErrInvalidOption)
	}

	h := &staticHandler{
		fsys:     fsys,
		onError:  o.onError,
		etags:    make(map[string]etag),
		prefix:   prefix,
		dotfiles: o.dotfiles,
	}

	if o.maxAge > 0 {
		h.cacheControl = "public, max-age=" + strconv.FormatInt(int64(o.maxAge/time.Second), 10)
	}

	return h, nil
}

// ServeHTTP serves the file the request's URL path names.
func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)

		return
	}

	name, ok := h.fileName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)

		return
	}

	h.serveFile(w, r, name)
}

// fileName returns the name in fsys of the file urlPath names, and false
// when it names none that may be served: it is outside the prefix, which,
// when empty, removes the leading `/` of urlPath if there is one, names
// the root or a directory by a trailing `/`, is not clean, with a `.` or
// `..` element or an empty one, is not valid UTF-8, holds a NUL byte or a
// backslash, or, unless dotfiles are served, has an element that starts
// with `.`.
func (h *staticHandler) fileName(urlPath string) (string, bool) {
	rest, ok := strings.CutPrefix(urlPath, h.prefix)
	if h.prefix == "" {
		rest = strings.TrimPrefix(urlPath, "/")
	}

	if !ok || rest == "" || !utf8.ValidString(rest) || strings.ContainsAny(rest, "\\\x00") ||
		"/"+rest != path.Clean("/"+rest) {
		return "", false
	}

	if !h.dotfiles && hasDotElement(rest) {
		return "", false
	}

	return rest, true
}

// hasDotElement reports whether an element of the slash path name starts
// with `.`.
func hasDotElement(name string) bool {
	for elem := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(elem, ".") {
			return true
		}
	}

	return false
}

// serveFile serves the regular file name of fsys, as http.ServeContent
// does: ranges, conditional requests and the content type, by extension
// or else sniffed.
func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	// Stat first, so that a FIFO or a device is never opened.
	info, err := fs.Stat(h.fsys, name)
	if err != nil {
		h.fail(w, r, err)

		return
	}

	if !info.Mode().IsRegular() {
		http.NotFound(w, r)

		return
	}

	f, err := h.fsys.Open(name)
	if err != nil {
		h.fail(w, r, err)

		return
	}

	// A file opened for reading has nothing to flush; its Close error
	// says nothing about what was served.
	defer func() { _ = f.Close() }()

	info, err = f.Stat()
	if err != nil {
		h.fail(w, r, err)

		return
	}

	if !info.Mode().IsRegular() {
		http.NotFound(w, r)

		return
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		h.fail(w, r, errNotSeeker)

		return
	}

	if info.ModTime().IsZero() {
		err = h.setETag(w, r, name, info.Size(), rs)
		if err != nil {
			h.fail(w, r, err)

			return
		}
	}

	if h.cacheControl != "" {
		w.Header().Set("Cache-Control", h.cacheControl)
	}

	http.ServeContent(w, r, name, info.ModTime(), rs)
}

// setETag sets the ETag of the file name of size bytes, whose content rs
// reads, on w, when etag gives one for r: a GET without a Range header
// may hash the file, a HEAD or a range request may not.
func (h *staticHandler) setETag(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	size int64,
	rs io.ReadSeeker,
) error {
	mayHash := r.Method == http.MethodGet && r.Header.Get("Range") == ""

	tag, err := h.etag(name, size, rs, mayHash)
	if err != nil {
		return err
	}

	if tag != "" {
		w.Header().Set("ETag", tag)
	}

	return nil
}

// errNotSeeker reports a file of an fs.FS that does not implement
// io.Seeker, which http.ServeContent needs.
var errNotSeeker = errors.New("file does not implement io.Seeker")

// etag returns the ETag of the file name of size bytes, whose content rs
// reads from its start: kept from an earlier request for a file of the
// same name and size, or else, when mayHash is true and the file has a
// tag kept or room for one among maxETags, a hash of the content, which
// it keeps. Otherwise it returns no tag, so that no request hashes a file
// whose tag it cannot keep. rs is at its start again on return.
func (h *staticHandler) etag(
	name string,
	size int64,
	rs io.ReadSeeker,
	mayHash bool,
) (string, error) {
	h.mu.Lock()

	e, ok := h.etags[name]
	full := len(h.etags) >= maxETags
	h.mu.Unlock()

	switch {
	case ok && e.size == size:
		return e.tag, nil
	case !mayHash, !ok && full:
		return "", nil
	}

	sum := sha256.New()

	_, err := io.Copy(sum, rs)
	if err != nil {
		return "", fmt.Errorf("hash %q: %w", name, err)
	}

	_, err = rs.Seek(0, io.SeekStart)
	if err != nil {
		return "", fmt.Errorf("hash %q: %w", name, err)
	}

	tag := `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`

	h.mu.Lock()

	if ok || len(h.etags) < maxETags {
		h.etags[name] = etag{tag: tag, size: size}
	}

	h.mu.Unlock()

	return tag, nil
}

// pathEscapes is the text of the error os.Root returns for a path, or a
// symlink, that leaves it. os does not export the error (errPathEscapes
// in os/file.go), so it is matched by text, and a test pins it.
const pathEscapes = "path escapes from parent"

// fail answers err, an error of a file: not found for a file that is not
// there, cannot be named, leaves the root, or may not be read, so that a
// response never tells a file that exists from one that does not; 503
// for a root that is closed; and else 500, which onError receives.
func (h *staticHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var pe *fs.PathError

	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrInvalid),
		errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ENAMETOOLONG),
		errors.Is(err, syscall.ELOOP), errors.Is(err, fs.ErrPermission),
		errors.As(err, &pe) && pe.Err.Error() == pathEscapes:
		http.NotFound(w, r)
	case errors.Is(err, os.ErrClosed):
		http.Error(w, "503 service unavailable", http.StatusServiceUnavailable)
	default:
		if h.onError != nil {
			h.onError(r, err)
		}

		http.Error(w, "500 internal server error", http.StatusInternalServerError)
	}
}
