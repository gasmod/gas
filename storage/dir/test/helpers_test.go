package dir_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// bucket is the bucket the helpers configure and create.
const bucket = "photos"

// build returns a store built with opts and a recording logger, not
// initialized.
func build(t *testing.T, opts ...dir.Option) (*dir.Provider, *recorder) {
	t.Helper()

	log := newRecorder()

	p, err := dir.New(opts...)(nil, log)
	require.NoError(t, err)

	return p, log
}

// config returns DefaultConfig with path as the directory and b as the
// configured bucket.
func config(path, b string) *dir.Config {
	cfg := dir.DefaultConfig()

	cfg.Storage.Dir = path
	cfg.Storage.Bucket = b

	return cfg
}

// open returns a store on a new directory, with bucket configured and
// created, initialized and closed when t ends, and the directory's path.
func open(t *testing.T) (*dir.Provider, string) {
	t.Helper()

	path := t.TempDir()

	return openAt(t, path), path
}

// openAt returns a store on the directory path, as open does.
func openAt(t *testing.T, path string, opts ...dir.Option) *dir.Provider {
	t.Helper()

	p, _ := build(t, append([]dir.Option{dir.WithConfig(config(path, bucket))}, opts...)...)
	require.NoError(t, p.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, p.Close(context.Background())) })

	return p
}

// sha returns the hex SHA-256 of s, the name a bucket or a key is stored
// under.
func sha(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

// metaFile returns the path of the metadata file of key in the
// configured bucket of the directory path.
func metaFile(path, key string) string {
	return filepath.Join(path, sha(bucket), sha(key)+".gas.json")
}

// objectsDir returns the path of the objects directory of the bucket b.
func objectsDir(path, b string) string {
	return filepath.Join(path, ".gas", "objects", sha(b))
}

// keysDir returns the path of the keys directory of the configured
// bucket.
func keysDir(path string) string {
	return filepath.Join(path, ".gas", "keys", sha(bucket))
}

// locksDir returns the path of the locks directory of the bucket b.
func locksDir(path, b string) string {
	return filepath.Join(path, ".gas", "locks", sha(b))
}

// nameRecord returns the path of the name record of the bucket b.
func nameRecord(path, b string) string {
	return filepath.Join(path, ".gas", "buckets", sha(b))
}

// dataFiles returns the names of the data files of key in the configured
// bucket.
func dataFiles(t *testing.T, path, key string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(objectsDir(path, bucket), sha(key)+"_*"))
	require.NoError(t, err)

	return files
}

// putIn returns the PutObjectInput of body under key in the configured
// bucket.
func putIn(key, body string) *storage.PutObjectInput {
	return &storage.PutObjectInput{
		Key:  key,
		Body: strings.NewReader(body),
	}
}

// put writes body under key in the configured bucket of p.
func put(t *testing.T, p *dir.Provider, key, body string) *storage.PutObjectOutput {
	t.Helper()

	out, err := p.PutObject(t.Context(), putIn(key, body))
	require.NoError(t, err)

	return out
}

// getIn returns the GetObjectInput of key in the configured bucket.
func getIn(key string) *storage.GetObjectInput {
	return &storage.GetObjectInput{
		Key: key,
	}
}

// headIn returns the HeadObjectInput of key in the configured bucket.
func headIn(key string) *storage.HeadObjectInput {
	return &getIn(key).HeadObjectInput
}

// read returns the contents of key in the configured bucket of p.
func read(t *testing.T, p *dir.Provider, key string) string {
	t.Helper()

	obj, err := p.GetObject(t.Context(), getIn(key))
	require.NoError(t, err)

	defer func() { require.NoError(t, obj.Body.Close()) }()

	b, err := io.ReadAll(obj.Body)
	require.NoError(t, err)

	return string(b)
}

// listKeys returns every key and common prefix of in, page by page, in
// byte order, the prefixes ending with `*`.
func listKeys(t *testing.T, p *dir.Provider, in *storage.ListObjectsInput) []string {
	t.Helper()

	var got []string

	for {
		out, err := p.ListObjects(t.Context(), in)
		require.NoError(t, err)

		page := make([]string, 0, len(out.Objects)+len(out.CommonPrefixes))

		for _, o := range out.Objects {
			page = append(page, o.Key)
		}

		for _, cp := range out.CommonPrefixes {
			page = append(page, cp+"*")
		}

		slices.SortFunc(page, func(a, b string) int {
			return strings.Compare(strings.TrimSuffix(a, "*"), strings.TrimSuffix(b, "*"))
		})

		got = append(got, page...)

		if !out.IsTruncated {
			return got
		}

		in.ContinuationToken = &out.NextContinuationToken
	}
}

// skip is an entry a skip handler got.
type skip struct {
	// err says why.
	err error
	// path is the entry's path.
	path string
}

// skips records what a skip handler gets. It is safe for concurrent use.
type skips struct {
	// got are the entries, in order.
	got []skip
	// mu guards got.
	mu sync.Mutex
}

// handler returns the skip handler that records into s.
func (s *skips) handler() dir.SkipHandler {
	return func(path string, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.got = append(s.got, skip{path: path, err: err})
	}
}

// all returns a copy of the entries so far.
func (s *skips) all() []skip {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.got)
}

// hexHash matches a name hash, which no error may hold.
var hexHash = regexp.MustCompile(`[0-9a-f]{64}`)

// requireClean fails t when err is nil, or its text holds a name hash,
// path or any of secrets, those that are not empty.
func requireClean(t *testing.T, err error, path string, secrets ...string) {
	t.Helper()

	require.Error(t, err)
	require.NotRegexp(t, hexHash, err.Error())

	for _, s := range append(secrets, path) {
		if s != "" {
			require.NotContains(t, err.Error(), s)
		}
	}
}

// blockingBody is a body whose Read blocks until its context ends.
type blockingBody struct {
	// ctx ends the Read.
	ctx context.Context //nolint:containedctx // what ends the Read.
	// entered is closed when the first Read starts.
	entered chan struct{}
	// once closes entered once.
	once sync.Once
}

// newBlockingBody returns a blockingBody that blocks until ctx ends.
func newBlockingBody(ctx context.Context) *blockingBody {
	return &blockingBody{ctx: ctx, entered: make(chan struct{})}
}

// Read blocks until ctx ends, then fails with its error.
func (b *blockingBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.ctx.Done()

	return 0, b.ctx.Err()
}

// slowBody is an endless body that yields a byte at a time, slowly.
type slowBody struct {
	// entered is closed when the first Read starts.
	entered chan struct{}
	// once closes entered once.
	once sync.Once
}

// newSlowBody returns a slowBody that has not been read.
func newSlowBody() *slowBody { return &slowBody{entered: make(chan struct{})} }

// Read waits a moment and yields one byte.
func (b *slowBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	time.Sleep(5 * time.Millisecond)

	if len(p) == 0 {
		return 0, nil
	}

	p[0] = 'x'

	return 1, nil
}

// entry is one log event a recorder received.
type entry struct {
	// fields are the event's fields, the logger's own included.
	fields map[string]any
	// level is `info`, `warn` and so on.
	level string
	// msg is the event's message.
	msg string
}

// recorder is a gas.Logger that records every event it sends. It is safe
// for concurrent use.
type recorder struct {
	// fields are the fields of the logger, from With.
	fields map[string]any
	// shared holds the events of the recorder and the loggers derived
	// from it.
	shared *recorded
}

// recorded is the event list a recorder and its derived loggers share.
type recorded struct {
	// entries are the events sent, in order.
	entries []entry
	// mu guards entries.
	mu sync.Mutex
}

var _ gas.Logger = (*recorder)(nil)

// newRecorder returns an empty recorder.
func newRecorder() *recorder { return &recorder{shared: &recorded{}} }

// Trace starts a trace event.
func (r *recorder) Trace(msg string) gas.LogEvent { return r.event("trace", msg) }

// Debug starts a debug event.
func (r *recorder) Debug(msg string) gas.LogEvent { return r.event("debug", msg) }

// Info starts an info event.
func (r *recorder) Info(msg string) gas.LogEvent { return r.event("info", msg) }

// Warn starts a warn event.
func (r *recorder) Warn(msg string) gas.LogEvent { return r.event("warn", msg) }

// Error starts an error event.
func (r *recorder) Error(msg string) gas.LogEvent { return r.event("error", msg) }

// With returns a context that derives a logger sharing r's events.
func (r *recorder) With() gas.LoggerContext {
	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recContext{parent: r, fields: fields}
}

// Flush does nothing.
func (*recorder) Flush() {}

// event returns an event of level and msg, with the logger's fields.
func (r *recorder) event(level, msg string) gas.LogEvent {
	fields := map[string]any{}
	maps.Copy(fields, r.fields)

	return &recEvent{r: r, e: entry{level: level, msg: msg, fields: fields}}
}

// entries returns a copy of the events sent so far.
func (r *recorder) entries() []entry {
	r.shared.mu.Lock()
	defer r.shared.mu.Unlock()

	return slices.Clone(r.shared.entries)
}

// recEvent is an event of a recorder being built.
type recEvent struct {
	// r is the recorder that sends it.
	r *recorder
	// e is the event.
	e entry
}

// Str adds a string field.
func (e *recEvent) Str(k, v string) gas.LogEvent { return e.set(k, v) }

// Int adds an int field.
func (e *recEvent) Int(k string, v int) gas.LogEvent { return e.set(k, v) }

// Int64 adds an int64 field.
func (e *recEvent) Int64(k string, v int64) gas.LogEvent { return e.set(k, v) }

// Float64 adds a float64 field.
func (e *recEvent) Float64(k string, v float64) gas.LogEvent { return e.set(k, v) }

// Bool adds a bool field.
func (e *recEvent) Bool(k string, v bool) gas.LogEvent { return e.set(k, v) }

// Err adds an error field.
func (e *recEvent) Err(k string, v error) gas.LogEvent { return e.set(k, v) }

// Duration adds a duration field.
func (e *recEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.set(k, v) }

// Any adds a field of any type.
func (e *recEvent) Any(k string, v any) gas.LogEvent { return e.set(k, v) }

// Send records the event.
func (e *recEvent) Send() {
	e.r.shared.mu.Lock()

	e.r.shared.entries = append(e.r.shared.entries, e.e)
	e.r.shared.mu.Unlock()
}

// set adds a field.
func (e *recEvent) set(k string, v any) gas.LogEvent {
	e.e.fields[k] = v

	return e
}

// recContext builds a logger derived from a recorder.
type recContext struct {
	// parent is the recorder derived from.
	parent *recorder
	// fields are the fields of the derived logger.
	fields map[string]any
}

// Str adds a string field.
func (c *recContext) Str(k, v string) gas.LoggerContext { return c.set(k, v) }

// Int adds an int field.
func (c *recContext) Int(k string, v int) gas.LoggerContext { return c.set(k, v) }

// Int64 adds an int64 field.
func (c *recContext) Int64(k string, v int64) gas.LoggerContext { return c.set(k, v) }

// Float64 adds a float64 field.
func (c *recContext) Float64(k string, v float64) gas.LoggerContext { return c.set(k, v) }

// Bool adds a bool field.
func (c *recContext) Bool(k string, v bool) gas.LoggerContext { return c.set(k, v) }

// Err adds an error field.
func (c *recContext) Err(k string, v error) gas.LoggerContext { return c.set(k, v) }

// Duration adds a duration field.
func (c *recContext) Duration(k string, v time.Duration) gas.LoggerContext {
	return c.set(k, v)
}

// Any adds a field of any type.
func (c *recContext) Any(k string, v any) gas.LoggerContext { return c.set(k, v) }

// Logger returns the derived logger, which shares the parent's events.
func (c *recContext) Logger() gas.Logger {
	return &recorder{fields: c.fields, shared: c.parent.shared}
}

// set adds a field.
func (c *recContext) set(k string, v any) gas.LoggerContext {
	c.fields[k] = v

	return c
}

// docLines returns the code lines of the doc comments in the Go file at
// path, without the comment marker and the tab, that start with prefix.
func docLines(t *testing.T, path, prefix string) []string {
	t.Helper()

	f, err := os.Open(path) //nolint:gosec // G304: the package's own doc.go.
	require.NoError(t, err)

	defer func() { require.NoError(t, f.Close()) }()

	var out []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		code, ok := strings.CutPrefix(sc.Text(), "//\t")
		if ok && strings.HasPrefix(code, prefix) {
			out = append(out, code)
		}
	}

	require.NoError(t, sc.Err())

	return out
}

// find returns the first event with msg, and whether there is one.
func (r *recorder) find(msg string) (entry, bool) {
	for _, e := range r.entries() {
		if e.msg == msg {
			return e, true
		}
	}

	return entry{}, false
}
