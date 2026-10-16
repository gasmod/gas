package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	dirstore "github.com/gasmod/gas/storage/dir"
	s3store "github.com/gasmod/gas/storage/s3"
)

// This file runs the same operations against the dir backend and the s3
// backend (talking to memS3, a small in-memory S3 emulation) and compares
// what a caller of storage.Provider observes.
//
// memS3 follows documented S3 behavior where it matters for the comparison:
// metadata keys are lowercased, an object stored without a Content-Type
// reads back as binary/octet-stream (not reached in practice: the transfer
// manager always sends one), the bucket is versioned (every PUT gets
// a version id, a DELETE without versionId creates a delete marker with its
// own version id), HEAD errors carry no body, and GET of an unknown version
// is 404 NoSuchVersion. A GET or HEAD honors a single "bytes=first-last",
// "bytes=first-" or "bytes=-suffix" range and ignores any other Range
// header; an unsatisfiable range is 416 InvalidRange. Buckets have owners, identified by the access key
// that signed the request: a PUT on a bucket creates it, or answers 409
// BucketAlreadyOwnedByYou for the caller's own bucket and 409
// BucketAlreadyExists for one another account owns. It is an emulation, not
// real S3.

const (
	contractBucket = "contract-bucket"

	// contractAccessKey is the access key the s3 contract backend signs
	// with, and so the account that owns the buckets it creates.
	contractAccessKey = "AKID"

	// foreignBucket is a bucket memS3 holds for another account.
	foreignBucket = "foreign-bucket"
)

// memObject is one stored object in memS3.
type memObject struct {
	modified    time.Time
	meta        map[string]string
	contentType string
	version     string
	body        []byte
}

// memS3 is an in-memory S3 emulation served over HTTP, holding objects per
// bucket and a counter for version ids.
type memS3 struct {
	buckets map[string]map[string]*memObject
	owners  map[string]string // bucket name to owning access key
	mu      sync.Mutex
	seq     int
}

// newMemS3 returns a memS3 holding the given empty buckets, owned by
// contractAccessKey, and foreignBucket, owned by another account.
func newMemS3(buckets ...string) *memS3 {
	m := &memS3{
		buckets: map[string]map[string]*memObject{foreignBucket: {}},
		owners:  map[string]string{foreignBucket: "OTHER-ACCOUNT"},
	}
	for _, b := range buckets {
		m.buckets[b] = map[string]*memObject{}
		m.owners[b] = contractAccessKey
	}
	return m
}

// requestAccessKey returns the access key in a SigV4 Authorization header
// ("... Credential=<key>/<scope>, ..."), or "" when there is none.
func requestAccessKey(r *http.Request) string {
	_, cred, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	if !ok {
		return ""
	}
	key, _, _ := strings.Cut(cred, "/")
	return key
}

// createBucket serves a PUT on a bucket: it creates the bucket for the
// signing account, or reports who already owns it. The caller holds m.mu.
func (m *memS3) createBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	caller := requestAccessKey(r)
	owner, exists := m.owners[bucket]
	switch {
	case !exists:
		m.buckets[bucket] = map[string]*memObject{}
		m.owners[bucket] = caller
		w.Header().Set("Location", "/"+bucket)
		w.WriteHeader(http.StatusOK)
	case owner == caller:
		memError(w, r, http.StatusConflict, "BucketAlreadyOwnedByYou")
	default:
		memError(w, r, http.StatusConflict, "BucketAlreadyExists")
	}
}

// memError writes an S3-style XML error with the given status and code.
// HEAD responses carry no body, as on S3.
func memError(w http.ResponseWriter, r *http.Request, status int, code string) {
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Error><Code>%s</Code><Message>%s</Message><RequestId>r</RequestId></Error>`, code, code)
}

// nextVersion returns a new unique version id. The caller holds m.mu.
func (m *memS3) nextVersion() string {
	m.seq++
	return fmt.Sprintf("v%06d", m.seq)
}

// ServeHTTP serves path-style bucket/key requests: a PUT on a bucket with no
// key creates it, then NoSuchBucket for an unknown bucket, then put, get or
// head, and delete (which records a delete marker version), and
// NotImplemented for any other method.
func (m *memS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key, hasKey := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	m.mu.Lock()
	defer m.mu.Unlock()

	if r.Method == http.MethodPut && (!hasKey || key == "") {
		m.createBucket(w, r, bucket)
		return
	}

	objs, ok := m.buckets[bucket]
	if !ok {
		memError(w, r, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch r.Method {
	case http.MethodPut:
		m.put(w, r, objs, key)
	case http.MethodGet, http.MethodHead:
		m.get(w, r, objs, key)
	case http.MethodDelete:
		delete(objs, key)
		w.Header().Set("X-Amz-Delete-Marker", "true")
		w.Header().Set("X-Amz-Version-Id", m.nextVersion())
		w.WriteHeader(http.StatusNoContent)
	default:
		memError(w, r, http.StatusNotImplemented, "NotImplemented")
	}
}

// put stores the request body as a new version of the object. Multipart
// uploads are not emulated and get NotImplemented.
func (m *memS3) put(
	w http.ResponseWriter,
	r *http.Request,
	objs map[string]*memObject,
	key string,
) {
	if r.URL.Query().Has("uploadId") {
		memError(w, r, http.StatusNotImplemented, "NotImplemented")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		memError(w, r, http.StatusBadRequest, "IncompleteBody")
		return
	}
	o := &memObject{
		body:        body,
		contentType: r.Header.Get("Content-Type"),
		meta:        map[string]string{},
		version:     m.nextVersion(),
		modified:    time.Now().UTC().Truncate(time.Second),
	}
	if o.contentType == "" {
		o.contentType = "binary/octet-stream"
	}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if name, ok := strings.CutPrefix(lk, "x-amz-meta-"); ok {
			o.meta[name] = v[0]
		}
	}
	objs[key] = o
	w.Header().Set("ETag", `"`+o.version+`"`)
	w.Header().Set("X-Amz-Version-Id", o.version)
	w.WriteHeader(http.StatusOK)
}

// get serves GET and HEAD for an object, honoring a versionId query.
func (m *memS3) get(
	w http.ResponseWriter,
	r *http.Request,
	objs map[string]*memObject,
	key string,
) {
	o, ok := objs[key]
	if v := r.URL.Query().Get("versionId"); v != "" && (!ok || o.version != v) {
		memError(w, r, http.StatusNotFound, "NoSuchVersion")
		return
	}
	if !ok {
		memError(w, r, http.StatusNotFound, "NoSuchKey")
		return
	}
	body, ranged, ok := memRange(r.Header.Get("Range"), o.body)
	if !ok {
		memError(w, r, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
		return
	}
	status := http.StatusOK
	if ranged {
		status = http.StatusPartialContent
	}
	h := w.Header()
	h.Set("Content-Length", fmt.Sprint(len(body)))
	h.Set("Content-Type", o.contentType)
	h.Set("Last-Modified", o.modified.Format(http.TimeFormat))
	h.Set("ETag", `"`+o.version+`"`)
	h.Set("X-Amz-Version-Id", o.version)
	for k, v := range o.meta {
		h["X-Amz-Meta-"+k] = []string{v}
	}
	w.WriteHeader(status)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// memRange applies a Range header to body the way memS3 emulates S3: a
// single first-last, first- or -suffix byte range is selected, clamped to
// the body, and ranged is true; any other header selects the whole body. ok
// is false for an unsatisfiable range: a first byte at or past the end, or a
// zero suffix.
func memRange(header string, body []byte) (sel []byte, ranged, ok bool) {
	spec, isBytes := strings.CutPrefix(header, "bytes=")
	first, last, isRange := strings.Cut(spec, "-")
	if !isBytes || !isRange || strings.Contains(spec, ",") {
		return body, false, true
	}
	size := int64(len(body))
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		switch {
		case err != nil:
			return body, false, true
		case n == 0:
			return nil, false, false
		}
		return body[max(0, size-n):], true, true
	}
	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return body, false, true
	}
	to := size - 1
	if last != "" {
		if to, err = strconv.ParseInt(last, 10, 64); err != nil || to < from {
			return body, false, true
		}
	}
	if from >= size {
		return nil, false, false
	}
	return body[from : min(to, size-1)+1], true, true
}

// backend is a provider under test with the name used in subtest names.
type backend struct {
	p    storage.Provider
	name string
}

// contractConfig returns a gas.ConfigProvider that sets contractBucket as the
// default bucket.
func contractConfig(t *testing.T) gas.ConfigProvider {
	t.Helper()
	mock, err := configtest.NewMockConfigWithValues(map[string]any{
		"storage": map[string]any{"bucket": contractBucket},
	})
	if err != nil {
		t.Fatalf("NewMockConfigWithValues() error: %v", err)
	}
	return mock
}

// newDirBackend returns a dir provider on a fresh root with contractBucket
// created.
func newDirBackend(t *testing.T) backend {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	p, err := dirstore.NewProvider(dirstore.WithRoot(root))(contractConfig(t))
	if err != nil {
		t.Fatalf("dir.NewProvider() error: %v", err)
	}
	if _, err := p.CreateBucket(context.Background(), &storage.CreateBucketInput{
		Bucket: contractBucket,
	}); err != nil {
		t.Fatalf("dir.CreateBucket() error: %v", err)
	}
	return backend{name: "dir", p: p}
}

// newS3Backend returns an s3 provider talking to a fresh memS3 that holds
// contractBucket.
func newS3Backend(t *testing.T) backend {
	t.Helper()
	srv := httptest.NewServer(newMemS3(contractBucket))
	t.Cleanup(srv.Close)
	client := awss3.New(awss3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			contractAccessKey,
			"SECRET",
			"",
		),
		HTTPClient:                 srv.Client(),
		Retryer:                    aws.NopRetryer{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	p, err := s3store.NewProvider(s3store.WithClient(client))(contractConfig(t))
	if err != nil {
		t.Fatalf("s3.NewProvider() error: %v", err)
	}
	return backend{name: "s3", p: p}
}

var backends = []struct {
	mk   func(*testing.T) backend
	name string
}{
	{name: "dir", mk: newDirBackend},
	{name: "s3", mk: newS3Backend},
}

// key returns an input for k in the default bucket.
func key(k string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Key: k}
}

// cput puts an object through p.
func cput(p storage.Provider, in storage.BaseObjectInput, body io.Reader, ct *string,
	meta map[string]string,
) (*storage.ObjectInfo, error) {
	return p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: in, Body: body, ContentType: ct, Metadata: meta,
	})
}

// chead heads an object through p.
func chead(p storage.Provider, in storage.BaseObjectInput) (*storage.ObjectInfo, error) {
	return p.HeadObject(context.Background(), &storage.HeadObjectInput{BaseObjectInput: in})
}

// cget gets an object through p and reads its whole body.
func cget(p storage.Provider, in storage.HeadObjectInput) (*storage.Object, []byte, error) {
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{HeadObjectInput: in})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = obj.Body.Close() }()
	data, err := io.ReadAll(obj.Body)
	return obj, data, err
}

// cdel deletes an object through p.
func cdel(p storage.Provider, in storage.BaseObjectInput) (*storage.ObjectInfo, error) {
	return p.DeleteObject(context.Background(), &storage.DeleteObjectInput{BaseObjectInput: in})
}

// classify reduces an error to the sentinels it matches so that results can
// be compared across backends.
func classify(err error) string {
	if err == nil {
		return "ok"
	}
	sentinels := []struct {
		err  error
		name string
	}{
		{storage.ErrInputRequired, "ErrInputRequired"},
		{storage.ErrInvalidKey, "ErrInvalidKey"},
		{storage.ErrInvalidBucket, "ErrInvalidBucket"},
		{storage.ErrNotFound, "ErrNotFound"},
		{storage.ErrKeyNotFound, "ErrKeyNotFound"},
		{storage.ErrBucketNotFound, "ErrBucketNotFound"},
		{storage.ErrVersionNotFound, "ErrVersionNotFound"},
		{storage.ErrBucketExists, "ErrBucketExists"},
		{storage.ErrNotSupported, "ErrNotSupported"},
		{storage.ErrInvalidRange, "ErrInvalidRange"},
	}
	var names []string
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			names = append(names, s.name)
		}
	}
	if len(names) == 0 {
		return "error"
	}
	return strings.Join(names, "+")
}

// metaString renders metadata with sorted keys, or <nil> for a nil map.
func metaString(m map[string]string) string {
	if m == nil {
		return "<nil>"
	}
	keys := slices.Sorted(maps.Keys(m))
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// TestContract_Common lists behavior both backends must share.
func TestContract_Common(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, p storage.Provider){
		"round trip":                       contractRoundTrip,
		"missing key":                      contractMissingKey,
		"missing bucket except head":       contractMissingBucket,
		"validation errors":                contractValidation,
		"overwrite replaces everything":    contractOverwrite,
		"prefix of a nested object":        contractPrefixOfNested,
		"keys are opaque":                  contractOpaqueKeys,
		"metadata not given":               contractNoMetadata,
		"read error with the 512th byte":   contractReadErrorWithData,
		"empty object":                     contractEmptyObject,
		"explicit content type round trip": contractExplicitContentType,
		"error message shape":              contractErrorMessage,
		"create bucket new then owned":     contractCreateBucket,
		"ranged reads":                     contractRange,
	}
	for name, check := range cases {
		for _, be := range backends {
			t.Run(name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				check(t, be.mk(t).p)
			})
		}
	}
}

// contractCreateBucket creates a new bucket, writes to it, and creates it
// again: a bucket the caller already owns is success and keeps its objects,
// in every backend.
func contractCreateBucket(t *testing.T, p storage.Provider) {
	ctx := context.Background()
	const bk = "contract-new-bucket"
	if _, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bk}); err != nil {
		t.Fatalf("CreateBucket(new) = %s (%v), want ok", classify(err), err)
	}
	in := storage.BaseObjectInput{Bucket: new(bk), Key: "kept"}
	if _, err := cput(p, in, strings.NewReader("kept"), nil, nil); err != nil {
		t.Fatalf("PutObject() into the new bucket error: %v", err)
	}
	info, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: bk})
	if err != nil {
		t.Fatalf("CreateBucket(owned) = %s (%v), want ok", classify(err), err)
	}
	if info == nil {
		t.Fatal("CreateBucket(owned) returned nil BucketInfo")
	}
	_, body, err := cget(p, storage.HeadObjectInput{BaseObjectInput: in})
	if err != nil || string(body) != "kept" {
		t.Errorf("GetObject() after CreateBucket(owned) = %q, %v; want kept", body, err)
	}
}

// contractRange reads ranges of a ten-byte and an empty object with GetObject
// and HeadObject, on the cases where S3 and RFC 9110 agree: the selected
// bytes and Size, or ErrInvalidRange.
func contractRange(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("ten"), strings.NewReader("0123456789"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cput(p, key("empty"), strings.NewReader(""), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ key, header, want string }{
		{"ten", "bytes=2-4", "234"},
		{"ten", "bytes=7-", "789"},
		{"ten", "bytes=-3", "789"},
		{"ten", "bytes=5-100", "56789"},
		{"ten", "bytes=-20", "0123456789"},
		{"ten", "bytes=10-", "ErrInvalidRange"},
		{"ten", "bytes=-0", "ErrInvalidRange"},
		{"ten", "garbage", "0123456789"},
		{"ten", "bytes=0-1,3-4", "0123456789"},
		{"empty", "bytes=-3", ""},
		{"empty", "bytes=0-0", "ErrInvalidRange"},
	} {
		in := storage.HeadObjectInput{BaseObjectInput: key(c.key), Range: new(c.header)}
		obj, body, err := cget(p, in)
		h, herr := p.HeadObject(context.Background(), &in)
		if c.want == "ErrInvalidRange" {
			if got := classify(err); got != c.want {
				t.Errorf("GetObject(%s, %q) = %s, want %s", c.key, c.header, got, c.want)
			}
			if got := classify(herr); got != c.want {
				t.Errorf("HeadObject(%s, %q) = %s, want %s", c.key, c.header, got, c.want)
			}
			continue
		}
		if err != nil || string(body) != c.want || obj.Size != int64(len(c.want)) {
			t.Errorf("GetObject(%s, %q) = %q, %v; want %q", c.key, c.header, body, err, c.want)
		}
		if herr != nil || h.Size != int64(len(c.want)) {
			t.Errorf(
				"HeadObject(%s, %q) = %+v, %v; want size %d",
				c.key,
				c.header,
				h,
				herr,
				len(c.want),
			)
		}
	}
}

// contractRoundTrip puts, heads, gets, and deletes one object.
func contractRoundTrip(t *testing.T, p storage.Provider) {
	body := bytes.Repeat([]byte("0123456789"), 100)
	meta := map[string]string{"owner": "ahmed", "trace-id": "t-1"}
	if _, err := cput(p, key("dir/obj.bin"), bytes.NewReader(body), new("application/x-test"),
		meta); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	h, err := chead(p, key("dir/obj.bin"))
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if h.Size != int64(len(body)) || h.ContentType != "application/x-test" ||
		!maps.Equal(h.Metadata, meta) {
		t.Errorf("Head = size %d type %q meta %v", h.Size, h.ContentType, h.Metadata)
	}
	_, data, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("dir/obj.bin")})
	if err != nil || !bytes.Equal(data, body) {
		t.Errorf("Get = %d bytes err %v, want %d bytes", len(data), err, len(body))
	}
	if _, err := cdel(p, key("dir/obj.bin")); err != nil {
		t.Errorf("DeleteObject() error: %v", err)
	}
	if _, err := chead(p, key("dir/obj.bin")); classify(err) != "ErrNotFound" {
		t.Errorf("Head after delete = %s, want ErrNotFound", classify(err))
	}
}

// contractMissingKey checks the sentinel each operation returns for a key
// that does not exist.
func contractMissingKey(t *testing.T, p storage.Provider) {
	if _, err := chead(p, key("nope")); classify(err) != "ErrNotFound" {
		t.Errorf("Head = %s (%v), want ErrNotFound", classify(err), err)
	}
	if _, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("nope")}); classify(
		err,
	) != "ErrKeyNotFound" {
		t.Errorf("Get = %s (%v), want ErrKeyNotFound", classify(err), err)
	}
	if _, err := cdel(p, key("nope")); err != nil {
		t.Errorf("Delete error = %v, want nil", err)
	}
}

// contractMissingBucket checks that object operations on a bucket that does
// not exist return ErrBucketNotFound.
func contractMissingBucket(t *testing.T, p storage.Provider) {
	in := storage.BaseObjectInput{Bucket: new("no-such-bucket"), Key: "k"}
	if _, err := cput(p, in, strings.NewReader("x"), nil, nil); classify(
		err,
	) != "ErrBucketNotFound" {
		t.Errorf("Put = %s (%v), want ErrBucketNotFound", classify(err), err)
	}
	if _, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: in}); classify(
		err,
	) != "ErrBucketNotFound" {
		t.Errorf("Get = %s (%v), want ErrBucketNotFound", classify(err), err)
	}
	if _, err := cdel(p, in); classify(err) != "ErrBucketNotFound" {
		t.Errorf("Delete = %s (%v), want ErrBucketNotFound", classify(err), err)
	}
}

// contractValidation checks that invalid keys, buckets, and nil inputs are
// rejected with their sentinels.
func contractValidation(t *testing.T, p storage.Provider) {
	cases := map[string]struct {
		in   storage.BaseObjectInput
		want string
	}{
		"nil key":      {in: storage.BaseObjectInput{}, want: "ErrInvalidKey"},
		"empty key":    {in: key(""), want: "ErrInvalidKey"},
		"dot key":      {in: key("."), want: "ErrInvalidKey"},
		"dot slash":    {in: key("./"), want: "ErrInvalidKey"},
		"trailing dot": {in: key("a/."), want: "ErrInvalidKey"},
		"too long key": {in: key(strings.Repeat("k", 1025)), want: "ErrInvalidKey"},
		"invalid bucket": {
			in:   storage.BaseObjectInput{Bucket: new("Bad"), Key: "k"},
			want: "ErrInvalidBucket",
		},
		"empty bucket": {
			in:   storage.BaseObjectInput{Bucket: new(""), Key: "k"},
			want: "ErrInvalidBucket",
		},
	}
	for name, tc := range cases {
		_, err := chead(p, tc.in)
		if got := classify(err); got != tc.want {
			t.Errorf("%s: Head = %s, want %s", name, got, tc.want)
		}
		_, err = cput(p, tc.in, nil, nil, nil)
		if got := classify(err); got != tc.want {
			t.Errorf("%s: Put = %s, want %s", name, got, tc.want)
		}
	}
	if _, err := p.HeadObject(context.Background(), nil); classify(err) != "ErrInputRequired" {
		t.Errorf("Head(nil) = %s, want ErrInputRequired", classify(err))
	}
}

// contractOverwrite checks that a second put replaces body, content type,
// and metadata together.
func contractOverwrite(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("k"), strings.NewReader("first"), new("text/x-1"),
		map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cput(p, key("k"), strings.NewReader("second"), new("text/x-2"),
		map[string]string{"b": "2"}); err != nil {
		t.Fatal(err)
	}
	obj, data, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("k")})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" || obj.ContentType != "text/x-2" ||
		metaString(obj.Metadata) != "{b=2}" {
		t.Errorf("after overwrite: %q %q %s", data, obj.ContentType, metaString(obj.Metadata))
	}
}

// contractPrefixOfNested checks that a prefix of an existing key is not an
// object, and deleting it leaves the longer key alone.
func contractPrefixOfNested(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("p/child"), strings.NewReader("x"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := chead(p, key("p")); classify(err) != "ErrNotFound" {
		t.Errorf("Head(p) = %s, want ErrNotFound", classify(err))
	}
	if _, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("p")}); classify(
		err,
	) != "ErrKeyNotFound" {
		t.Errorf("Get(p) = %s, want ErrKeyNotFound", classify(err))
	}
	if _, err := cdel(p, key("p")); err != nil {
		t.Errorf("Delete(p) error = %v", err)
	}
	if _, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("p/child")}); err != nil {
		t.Errorf("Get(p/child) after Delete(p) error = %v", err)
	}
}

// contractOpaqueKeys checks that a key is an opaque string: keys that look
// like paths, differ only in case, or share a prefix are all distinct objects.
func contractOpaqueKeys(t *testing.T, p storage.Provider) {
	for _, k := range []string{
		"../escape", "/abs", ".__gas_metadata__x", "p/c", "p", "a/b",
		`a\.`, `.\`, `\`, `a\..`,
	} {
		if _, err := cput(p, key(k), strings.NewReader(k), nil, nil); err != nil {
			t.Errorf("Put(%q) error = %v", k, err)
		}
	}
	if got := observeHeadAfterPut("a/b", "a/b/")(t, p); got != "ErrNotFound" {
		t.Errorf("Head(a/b/) after Put(a/b) = %s, want ErrNotFound", got)
	}
	if got := observeCaseKeys(t, p); got != "distinct" {
		t.Errorf("keys differing only in case = %s, want distinct", got)
	}
}

// contractNoMetadata checks that an object put without metadata reads back
// with nil metadata.
func contractNoMetadata(t *testing.T, p storage.Provider) {
	if got := observeMeta(nil)(t, p); got != "<nil>" {
		t.Errorf("metadata not given = %s, want <nil>", got)
	}
}

// contractReadErrorWithData checks that a read error returned together
// with data fails the put.
func contractReadErrorWithData(t *testing.T, p storage.Provider) {
	if got := observeReadErrorWithData(t, p); got != "error" {
		t.Errorf("Put with a read error on the 512th byte = %s, want error", got)
	}
}

// contractEmptyObject checks that a nil body stores an empty object.
func contractEmptyObject(t *testing.T, p storage.Provider) {
	if _, err := cput(p, key("e"), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	obj, data, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("e")})
	if err != nil || obj.Size != 0 || len(data) != 0 {
		t.Errorf("empty object: size %d len %d err %v", obj.Size, len(data), err)
	}
}

// contractExplicitContentType checks that a given content type reads back
// unchanged.
func contractExplicitContentType(t *testing.T, p storage.Provider) {
	for _, ct := range []string{"text/html; charset=utf-8", "application/json", "x/y+z"} {
		if _, err := cput(p, key("ct"), strings.NewReader("<b>"), new(ct), nil); err != nil {
			t.Fatal(err)
		}
		h, err := chead(p, key("ct"))
		if err != nil || h.ContentType != ct {
			t.Errorf("ContentType = %q err %v, want %q", h.ContentType, err, ct)
		}
	}
}

// contractErrorMessage checks that both backends format an error the same
// way: module prefix, operation, then the storage sentinel's text, then the
// cause.
func contractErrorMessage(t *testing.T, p storage.Provider) {
	_, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("nope")})
	if err == nil {
		t.Fatal("Get of a missing key succeeded")
	}
	want := "/provider: get: " + storage.ErrKeyNotFound.Error() + ": "
	if !strings.HasPrefix(err.Error(), "gas/storage/") || !strings.Contains(err.Error(), want) {
		t.Errorf("Get error = %q, want gas/storage/<backend>%s<cause>", err, want)
	}
}

// divergence records a behavior where the backends differ. want holds the
// currently observed (and reviewed) result per backend; a change in either
// backend fails the test so the divergence gets re-reviewed.
type divergence struct {
	observe func(t *testing.T, p storage.Provider) string
	want    map[string]string
	name    string
}

// TestContract_Divergences checks every documented divergence against what
// each backend does now.
func TestContract_Divergences(t *testing.T) {
	t.Parallel()

	for _, d := range divergences(t) {
		for _, be := range backends {
			t.Run(d.name+"/"+be.name, func(t *testing.T) {
				t.Parallel()
				got := d.observe(t, be.mk(t).p)
				if want := d.want[be.name]; got != want {
					t.Errorf("%s on %s: observed %q, documented %q", d.name, be.name, got, want)
				}
			})
		}
	}
}

// divergences returns the reviewed behaviors where the backends differ.
func divergences(t *testing.T) []divergence {
	t.Helper()
	return []divergence{
		{
			// dir sniffs; the s3 transfer manager sends application/octet-stream
			// itself, so S3's own default never applies.
			name:    "content type when none is given",
			observe: observeDefaultContentType([]byte("hello world")),
			want: map[string]string{
				"dir": "text/plain; charset=utf-8",
				"s3":  "application/octet-stream",
			},
		},
		{
			name:    "metadata key case",
			observe: observeMeta(map[string]string{"Owner": "x", "Trace-ID": "y"}),
			want:    map[string]string{"dir": "{Owner=x,Trace-ID=y}", "s3": "{owner=x,trace-id=y}"},
		},
		{
			name:    "metadata that is not a valid HTTP header",
			observe: observeMeta(map[string]string{"bad key": "v"}),
			want:    map[string]string{"dir": "{bad key=v}", "s3": "put error"},
		},
		{
			name:    "invalid utf-8 metadata value",
			observe: observeMeta(map[string]string{"k": "a\xffb"}),
			want:    map[string]string{"dir": "{k=a�b}", "s3": "{k=a\xffb}"},
		},
		{
			name:    "head on missing bucket",
			observe: observeHeadMissingBucket,
			want:    map[string]string{"dir": "ErrBucketNotFound", "s3": "ErrNotFound"},
		},
		{
			name:    "put returns a version",
			observe: observeVersionConsistent,
			want:    map[string]string{"dir": "none", "s3": "consistent"},
		},
		{
			name:    "delete returns version",
			observe: observeDeleteVersion,
			want:    map[string]string{"dir": "empty", "s3": "new version"},
		},
		{
			name:    "get with unknown version",
			observe: observeGetUnknownVersion,
			want:    map[string]string{"dir": "ErrNotSupported", "s3": "ErrVersionNotFound"},
		},
		{
			// A HEAD 404 carries no error code, so s3 cannot say NoSuchVersion.
			name:    "head with unknown version",
			observe: observeHeadUnknownVersion,
			want:    map[string]string{"dir": "ErrNotSupported", "s3": "ErrNotFound"},
		},

		{
			// dir has no other accounts: every name under the root is the
			// caller's, so the name is simply created.
			name:    "create bucket on a name another account owns",
			observe: observeCreateForeignBucket,
			want:    map[string]string{"dir": "ok", "s3": "ErrBucketExists"},
		},
		{
			name:    "last modified precision",
			observe: observeLastModifiedPrecision,
			want:    map[string]string{"dir": "sub-second", "s3": "whole seconds"},
		},
	}
}

// observeCreateForeignBucket reports how CreateBucket classifies
// foreignBucket, which memS3 holds for another account.
func observeCreateForeignBucket(_ *testing.T, p storage.Provider) string {
	_, err := p.CreateBucket(
		context.Background(),
		&storage.CreateBucketInput{Bucket: foreignBucket},
	)
	return classify(err)
}

// observeVersionConsistent reports whether put, head, and get agree on a
// version, or that the backend returns none.
func observeVersionConsistent(t *testing.T, p storage.Provider) string {
	put, err := cput(p, key("v"), strings.NewReader("x"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := chead(p, key("v"))
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("v")})
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case put.Version == "" && h.Version == "" && obj.Version == "":
		return "none"
	case put.Version != "" && h.Version == put.Version && obj.Version == put.Version:
		return "consistent"
	default:
		return fmt.Sprintf("put=%q head=%q get=%q", put.Version, h.Version, obj.Version)
	}
}

// observeDefaultContentType returns the content type stored for body when
// the put gives none.
func observeDefaultContentType(body []byte) func(*testing.T, storage.Provider) string {
	return func(t *testing.T, p storage.Provider) string {
		if _, err := cput(p, key("k"), bytes.NewReader(body), nil, nil); err != nil {
			t.Fatal(err)
		}
		h, err := chead(p, key("k"))
		if err != nil {
			t.Fatal(err)
		}
		return h.ContentType
	}
}

// observeMeta returns the metadata read back after putting meta, or the
// put's error class.
func observeMeta(meta map[string]string) func(*testing.T, storage.Provider) string {
	return func(t *testing.T, p storage.Provider) string {
		if _, err := cput(p, key("k"), strings.NewReader("x"), nil, meta); err != nil {
			return "put " + classify(err)
		}
		h, err := chead(p, key("k"))
		if err != nil {
			t.Fatal(err)
		}
		return metaString(h.Metadata)
	}
}

// observeHeadMissingBucket returns the error class of a head in a bucket
// that does not exist.
func observeHeadMissingBucket(_ *testing.T, p storage.Provider) string {
	_, err := chead(p, storage.BaseObjectInput{Bucket: new("no-such-bucket"), Key: "k"})
	return classify(err)
}

// observeDeleteVersion reports which version a delete returns.
func observeDeleteVersion(t *testing.T, p storage.Provider) string {
	put, err := cput(p, key("k"), strings.NewReader("x"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := cdel(p, key("k"))
	if err != nil {
		t.Fatal(err)
	}
	switch d.Version {
	case "":
		return "empty"
	case put.Version:
		return "deleted object's version"
	default:
		return "new version"
	}
}

// observeGetUnknownVersion returns the error class of a get with a version
// that was never written.
func observeGetUnknownVersion(t *testing.T, p storage.Provider) string {
	if _, err := cput(p, key("k"), strings.NewReader("x"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("k"), Version: new("bogus")})
	return classify(err)
}

// observeHeadUnknownVersion returns the error class of a head with a version
// that was never written.
func observeHeadUnknownVersion(t *testing.T, p storage.Provider) string {
	if _, err := cput(p, key("k"), strings.NewReader("x"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := p.HeadObject(context.Background(), &storage.HeadObjectInput{
		BaseObjectInput: key("k"), Version: new("bogus"),
	})
	return classify(err)
}

// observeHeadAfterPut returns the error class of heading headKey after
// putting putKey.
func observeHeadAfterPut(putKey, headKey string) func(*testing.T, storage.Provider) string {
	return func(t *testing.T, p storage.Provider) string {
		if _, err := cput(p, key(putKey), strings.NewReader("x"), nil, nil); err != nil {
			t.Fatal(err)
		}
		_, err := chead(p, key(headKey))
		return classify(err)
	}
}

// observeCaseKeys reports whether keys differing only in case are distinct
// objects.
func observeCaseKeys(t *testing.T, p storage.Provider) string {
	if _, err := cput(p, key("Foo"), strings.NewReader("A"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cput(p, key("foo"), strings.NewReader("B"), nil, nil); err != nil {
		t.Fatal(err)
	}
	_, data, err := cget(p, storage.HeadObjectInput{BaseObjectInput: key("Foo")})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "A" {
		return "distinct"
	}
	return "aliased"
}

// dataThenErr is a reader that returns its data together with an error.
type dataThenErr struct {
	data []byte
	done bool
}

// Read returns all data with an error on the first call and io.EOF after.
func (d *dataThenErr) Read(b []byte) (int, error) {
	if d.done {
		return 0, io.EOF
	}
	d.done = true
	return copy(b, d.data), errors.New("boom")
}

// observeReadErrorWithData returns the error class of a put whose reader
// fails on the call that returns the 512th byte.
func observeReadErrorWithData(_ *testing.T, p storage.Provider) string {
	_, err := cput(p, key("k"), &dataThenErr{data: bytes.Repeat([]byte("z"), 512)}, nil, nil)
	return classify(err)
}

// observeLastModifiedPrecision reports whether LastModified keeps
// sub-second precision.
func observeLastModifiedPrecision(t *testing.T, p storage.Provider) string {
	// Retry a few times so a dir write that lands exactly on a second
	// boundary does not flip the observation.
	for range 3 {
		if _, err := cput(p, key("k"), strings.NewReader("x"), nil, nil); err != nil {
			t.Fatal(err)
		}
		h, err := chead(p, key("k"))
		if err != nil {
			t.Fatal(err)
		}
		if h.LastModified.Nanosecond() != 0 {
			return "sub-second"
		}
	}
	return "whole seconds"
}
