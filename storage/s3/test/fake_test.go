package s3_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: S3's ETags and Content-MD5 are MD5s.
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/listing"
	s3store "github.com/gasmod/gas/storage/s3"
)

// The credentials and the region the fake accepts, and the access key ID
// of the account that owns its foreign buckets.
const (
	testKeyID   = "AKIDTEST"
	testSecret  = "zqxsecret"
	testRegion  = "us-east-1"
	foreignKey  = "AKIDOTHER"
	amzDate     = "20060102T150405Z"
	listingTime = "2006-01-02T15:04:05.000Z"
)

// fakeObject is an object the fake stores.
type fakeObject struct {
	// modified is when it was written.
	modified time.Time
	// header holds its header fields: Content-Type and the others S3
	// stores.
	header http.Header
	// meta is its user metadata, names in lower case.
	meta map[string]string
	// etag is its ETag, unquoted.
	etag string
	// version is its version ID.
	version string
	// data is its contents.
	data []byte
}

// fakeBucket is a bucket the fake holds.
type fakeBucket struct {
	// created is when it was created.
	created time.Time
	// objects holds its objects by key.
	objects map[string]*fakeObject
	// owner is the access key ID of the account that owns it.
	owner string
}

// fakeUpload is an upload in parts in progress.
type fakeUpload struct {
	// header holds the header fields of the object it writes.
	header http.Header
	// meta is the user metadata of the object it writes.
	meta map[string]string
	// parts holds the parts uploaded, by number.
	parts map[int][]byte
	// bucket and key name the object it writes.
	bucket, key string
}

// fakeS3 is an S3 server in memory, served over HTTP with path-style
// requests: buckets with owners, objects with the header fields and
// metadata S3 stores, conditional writes, ranges, ListObjectsV2 and
// ListBuckets with their pages, uploads in parts, and presigned URLs whose
// SigV4 signature and expiry it checks. A request signed with another key
// than testKeyID is refused, as S3 refuses one it cannot authenticate. It
// is safe for concurrent use.
type fakeS3 struct {
	// hook, when set, sees every request first, and answers it when it
	// returns true.
	hook func(w http.ResponseWriter, r *http.Request) bool
	// srv serves the fake.
	srv *httptest.Server
	// buckets holds the buckets by name.
	buckets map[string]*fakeBucket
	// uploads holds the uploads in progress by ID.
	uploads map[string]*fakeUpload
	// ops counts the requests by operation, such as `PutObject`.
	ops map[string]int
	// mu guards every field but srv and hook.
	mu sync.Mutex
	// seq numbers versions and uploads.
	seq int
}

// newFake returns a running fake, closed when the test ends, holding a
// bucket `foreign` of another account.
func newFake(tb testing.TB) *fakeS3 {
	tb.Helper()

	f := &fakeS3{
		buckets: map[string]*fakeBucket{
			"foreign": {created: time.Now(), objects: map[string]*fakeObject{}, owner: foreignKey},
		},
		uploads: map[string]*fakeUpload{},
		ops:     map[string]int{},
	}

	f.srv = httptest.NewServer(f)
	tb.Cleanup(f.srv.Close)

	return f
}

// ServeHTTP answers r as S3 would.
func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if hook := f.currentHook(); hook != nil && hook(w, r) {
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		fakeError(w, r, http.StatusBadRequest, "IncompleteBody")

		return
	}

	if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		fakeError(w, r, http.StatusNotImplemented, "NotImplemented")

		return
	}

	if !f.authorized(w, r) {
		return
	}

	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case bucket == "":
		f.listBuckets(w, r)
	case key == "":
		f.bucketOp(w, r, bucket, body)
	default:
		f.objectOp(w, r, bucket, key, body)
	}
}

// config returns DefaultConfig pointed at the fake, with the test region
// and credentials, and bucket configured.
func (f *fakeS3) config(bucket string) *s3store.Config {
	cfg := s3store.DefaultConfig()

	cfg.Storage.Region = testRegion
	cfg.Storage.Endpoint = f.srv.URL
	cfg.Storage.AccessKeyID = testKeyID
	cfg.Storage.SecretAccessKey = testSecret
	cfg.Storage.Bucket = bucket

	return cfg
}

// count returns how many requests of op the fake received.
func (f *fakeS3) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ops[op]
}

// pending returns how many uploads in parts are in progress.
func (f *fakeS3) pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.uploads)
}

// currentHook returns the hook, under the lock.
func (f *fakeS3) currentHook() func(http.ResponseWriter, *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hook
}

// setHook sets the hook, under the lock.
func (f *fakeS3) setHook(hook func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.hook = hook
}

// count records a request of op. The caller holds f.mu.
func (f *fakeS3) record(op string) { f.ops[op]++ }

// authorized checks the signature of a presigned request, or the access
// key of another, and answers r with the error S3 sends when it fails.
func (f *fakeS3) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Has("X-Amz-Signature") {
		return f.presigned(w, r)
	}

	_, cred, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	if !ok {
		fakeError(w, r, http.StatusForbidden, "AccessDenied")

		return false
	}

	if key, _, _ := strings.Cut(cred, "/"); key != testKeyID {
		fakeError(w, r, http.StatusForbidden, "InvalidAccessKeyId")

		return false
	}

	return true
}

// presigned checks a presigned request: unexpired, and signed by
// testKeyID over the method, the URL and the header fields it names, the
// Content-Length the client sends included.
func (f *fakeS3) presigned(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()

	signed, err := time.Parse(amzDate, q.Get("X-Amz-Date"))
	if err != nil {
		fakeError(w, r, http.StatusForbidden, "AccessDenied")

		return false
	}

	ttl, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || time.Now().After(signed.Add(time.Duration(ttl)*time.Second)) {
		fakeError(w, r, http.StatusForbidden, "AccessDenied")

		return false
	}

	scope := strings.Split(q.Get("X-Amz-Credential"), "/")
	if len(scope) != 5 || scope[0] != testKeyID {
		fakeError(w, r, http.StatusForbidden, "InvalidAccessKeyId")

		return false
	}

	if signature(r, signed, scope[2]) != q.Get("X-Amz-Signature") {
		fakeError(w, r, http.StatusForbidden, "SignatureDoesNotMatch")

		return false
	}

	return true
}

// signature returns the SigV4 signature of the presigned request r, signed
// at signed in region, as the server computes it from what r carries.
//
//nolint:cyclop // one case per request or rule S3 serves.
func signature(r *http.Request, signed time.Time, region string) string {
	rest := url.Values{}

	for k, v := range r.URL.Query() {
		switch k {
		case "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-SignedHeaders",
			"X-Amz-Signature", "X-Amz-Security-Token":
		default:
			rest[k] = v
		}
	}

	u := *r.URL

	u.Scheme, u.Host, u.RawQuery = "http", r.Host, rest.Encode()

	//nolint:gosec // G704: the request is signed, never sent.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		return ""
	}

	for h := range strings.SplitSeq(r.URL.Query().Get("X-Amz-SignedHeaders"), ";") {
		switch h {
		case "host":
			req.Host = r.Host
		case "content-length":
			req.ContentLength = r.ContentLength
		default:
			req.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}

	uri, _, err := v4.NewSigner().PresignHTTP(r.Context(),
		aws.Credentials{AccessKeyID: testKeyID, SecretAccessKey: testSecret},
		req, "UNSIGNED-PAYLOAD", "s3", region, signed,
		func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err != nil {
		return ""
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}

	return parsed.Query().Get("X-Amz-Signature")
}

// fakeError answers r with an S3 error reply of status and code, whose
// message and resource quote the request's path, as S3's do. A HEAD
// reply has no body.
func fakeError(w http.ResponseWriter, r *http.Request, status int, code string) {
	if r.Method == http.MethodHead {
		w.WriteHeader(status)

		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	//nolint:gosec // G705: the fake's reply, read by the tests' client only.
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Error><Code>%s</Code><Message>%s of %s</Message><Resource>%s</Resource>`+
		`<RequestId>zqxrequest</RequestId></Error>`,
		code, code, r.URL.Path, r.URL.Path)
}

// writeXML answers with v as XML, with status 200.
func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")

	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

// bucketOp answers a request on a bucket: CreateBucket, HeadBucket,
// DeleteBucket and ListObjectsV2. The caller holds f.mu.
//
//nolint:cyclop // one case per request or rule S3 serves.
func (f *fakeS3) bucketOp(w http.ResponseWriter, r *http.Request, name string, _ []byte) {
	b, ok := f.buckets[name]

	switch {
	case r.Method == http.MethodPut:
		f.record("CreateBucket")
		f.createBucket(w, r, name, b)
	case !ok:
		fakeError(w, r, http.StatusNotFound, "NoSuchBucket")
	case b.owner != testKeyID:
		fakeError(w, r, http.StatusForbidden, "AccessDenied")
	case r.Method == http.MethodHead:
		f.record("HeadBucket")
		w.Header().Set("X-Amz-Bucket-Region", testRegion)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && len(b.objects) > 0:
		fakeError(w, r, http.StatusConflict, "BucketNotEmpty")
	case r.Method == http.MethodDelete:
		f.record("DeleteBucket")
		delete(f.buckets, name)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.record("ListObjectsV2")
		f.listObjects(w, r, b)
	default:
		fakeError(w, r, http.StatusNotImplemented, "NotImplemented")
	}
}

// createBucket creates the bucket name, or tells who owns it. The caller
// holds f.mu.
func (f *fakeS3) createBucket(w http.ResponseWriter, r *http.Request, name string, b *fakeBucket) {
	switch {
	case b == nil:
		f.buckets[name] = &fakeBucket{
			created: time.Now(),
			objects: map[string]*fakeObject{},
			owner:   testKeyID,
		}

		w.WriteHeader(http.StatusOK)
	case b.owner == testKeyID:
		fakeError(w, r, http.StatusConflict, "BucketAlreadyOwnedByYou")
	default:
		fakeError(w, r, http.StatusConflict, "BucketAlreadyExists")
	}
}

// objectOp answers a request on an object. The caller holds f.mu.
//
//nolint:cyclop // one case per request or rule S3 serves.
func (f *fakeS3) objectOp(w http.ResponseWriter, r *http.Request, bucket, key string, body []byte) {
	b, ok := f.buckets[bucket]
	q := r.URL.Query()

	switch {
	case !ok:
		fakeError(w, r, http.StatusNotFound, "NoSuchBucket")
	case b.owner != testKeyID:
		fakeError(w, r, http.StatusForbidden, "AccessDenied")
	case r.Method == http.MethodPost && q.Has("uploads"):
		f.record("CreateMultipartUpload")
		f.createUpload(w, r, bucket, key)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		f.record("UploadPart")
		f.uploadPart(w, r, body)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		f.record("CompleteMultipartUpload")
		f.complete(w, r, b, body)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		f.record("AbortMultipartUpload")
		f.abort(w, r)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		f.record("CopyObject")
		f.copyObject(w, r, b, key)
	case r.Method == http.MethodPut:
		f.record("PutObject")
		f.put(w, r, b, key, body)
	case r.Method == http.MethodGet, r.Method == http.MethodHead:
		f.record(
			map[string]string{http.MethodGet: "GetObject", http.MethodHead: "HeadObject"}[r.Method],
		)
		f.get(w, r, b.objects[key])
	case r.Method == http.MethodDelete:
		f.record("DeleteObject")
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		fakeError(w, r, http.StatusNotImplemented, "NotImplemented")
	}
}

// storedHeaders are the header fields S3 stores with an object.
var storedHeaders = []string{
	"Content-Type", "Cache-Control", "Content-Disposition", "Content-Encoding",
	"Content-Language", "Expires",
}

// headersOf returns the header fields of r S3 stores, and its user
// metadata, names in lower case.
func headersOf(r *http.Request) (http.Header, map[string]string) {
	h := http.Header{}

	for _, name := range storedHeaders {
		if v := r.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}

	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "binary/octet-stream")
	}

	meta := map[string]string{}

	for name, v := range r.Header {
		if m, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok {
			meta[m] = v[0]
		}
	}

	return h, meta
}

// condition reports whether the If-None-Match and If-Match of r hold over
// cur, the object a write would replace, and answers r when they do not.
func condition(w http.ResponseWriter, r *http.Request, cur *fakeObject) bool {
	if r.Header.Get("If-None-Match") == "*" && cur != nil {
		fakeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed")

		return false
	}

	if m := r.Header.Get("If-Match"); m != "" {
		if cur == nil {
			fakeError(w, r, http.StatusNotFound, "NoSuchKey")

			return false
		}

		if m != `"`+cur.etag+`"` {
			fakeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed")

			return false
		}
	}

	return true
}

// md5Hex returns the hex MD5 of data.
func md5Hex(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // G401: an ETag.

	return hex.EncodeToString(sum[:])
}

// store stores data under key in b with r's header fields, and answers
// with its ETag and version. The caller holds f.mu.
func (f *fakeS3) store(
	w http.ResponseWriter,
	b *fakeBucket,
	key string,
	o *fakeObject,
) {
	f.seq++

	o.version = "v" + strconv.Itoa(f.seq)
	o.modified = time.Now()
	b.objects[key] = o

	w.Header().Set("ETag", `"`+o.etag+`"`)
	w.Header().Set("X-Amz-Version-Id", o.version)
}

// put stores the body of r under key in b. The caller holds f.mu.
func (f *fakeS3) put(
	w http.ResponseWriter,
	r *http.Request,
	b *fakeBucket,
	key string,
	body []byte,
) {
	if md := r.Header.Get("Content-MD5"); md != "" {
		sum := md5.Sum(body) //nolint:gosec // G401: Content-MD5.
		if md != base64.StdEncoding.EncodeToString(sum[:]) {
			fakeError(w, r, http.StatusBadRequest, "BadDigest")

			return
		}
	}

	if !condition(w, r, b.objects[key]) {
		return
	}

	h, meta := headersOf(r)

	f.store(w, b, key, &fakeObject{header: h, meta: meta, etag: md5Hex(body), data: body})
	w.WriteHeader(http.StatusOK)
}

// copyResult is the reply of CopyObject.
type copyResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

// copyObject copies the object x-amz-copy-source names to key in b, with
// its header fields and metadata, or those of r under the REPLACE
// metadata directive, honoring x-amz-copy-source-if-match, and refusing
// a copy onto itself without REPLACE, as S3 does. The caller holds f.mu.
//
//nolint:cyclop // one case per rule S3 serves a copy with.
func (f *fakeS3) copyObject(w http.ResponseWriter, r *http.Request, b *fakeBucket, key string) {
	source, err := url.PathUnescape(r.Header.Get("X-Amz-Copy-Source"))
	srcBucket, srcKey, ok := strings.Cut(strings.TrimPrefix(source, "/"), "/")

	if err != nil || !ok {
		fakeError(w, r, http.StatusBadRequest, "InvalidArgument")

		return
	}

	sb, ok := f.buckets[srcBucket]

	switch {
	case !ok:
		fakeError(w, r, http.StatusNotFound, "NoSuchBucket")

		return
	case sb.owner != testKeyID:
		fakeError(w, r, http.StatusForbidden, "AccessDenied")

		return
	}

	src := sb.objects[srcKey]
	replace := r.Header.Get("X-Amz-Metadata-Directive") == "REPLACE"

	switch m := r.Header.Get("X-Amz-Copy-Source-If-Match"); {
	case src == nil:
		fakeError(w, r, http.StatusNotFound, "NoSuchKey")

		return
	case m != "" && m != `"`+src.etag+`"`:
		fakeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed")

		return
	case !replace && sb == b && srcKey == key:
		fakeError(w, r, http.StatusBadRequest, "InvalidRequest")

		return
	}

	o := &fakeObject{
		header: src.header.Clone(),
		meta:   maps.Clone(src.meta),
		etag:   src.etag,
		data:   src.data,
	}
	if replace {
		o.header, o.meta = headersOf(r)
	}

	f.store(w, b, key, o)
	writeXML(w, copyResult{
		ETag:         `"` + o.etag + `"`,
		LastModified: o.modified.UTC().Format(listingTime),
	})
}

// get answers a GetObject or a HeadObject of o, honoring a Range, a
// versionId and the response-* overrides.
//
//nolint:cyclop,funlen // one case per rule S3 serves a read with.
func (*fakeS3) get(w http.ResponseWriter, r *http.Request, o *fakeObject) {
	q := r.URL.Query()

	switch v := q.Get("versionId"); {
	case v != "" && (o == nil || o.version != v):
		fakeError(w, r, http.StatusNotFound, "NoSuchVersion")

		return
	case o == nil:
		fakeError(w, r, http.StatusNotFound, "NoSuchKey")

		return
	}

	data, status := o.data, http.StatusOK

	if rng := r.Header.Get("Range"); rng != "" {
		br, err := storage.ParseRange(rng)
		if err != nil {
			fakeError(w, r, http.StatusBadRequest, "InvalidArgument")

			return
		}

		off, n, err := br.Resolve(int64(len(data)))
		if err != nil {
			fakeError(w, r, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")

			return
		}

		if n > 0 {
			status = http.StatusPartialContent

			w.Header().Set("Content-Range",
				fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, len(data)))
		}

		data = data[off : off+n]
	}

	h := w.Header()
	maps.Copy(h, o.header)

	for _, name := range storedHeaders {
		if v := q.Get("response-" + strings.ToLower(name)); v != "" {
			h.Set(name, v)
		}
	}

	for k, v := range o.meta {
		h["X-Amz-Meta-"+k] = []string{v}
	}

	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("ETag", `"`+o.etag+`"`)
	h.Set("Last-Modified", o.modified.UTC().Format(http.TimeFormat))
	h.Set("X-Amz-Version-Id", o.version)
	w.WriteHeader(status)

	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

// initiateResult is the reply of CreateMultipartUpload.
type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

// createUpload begins an upload in parts. The caller holds f.mu.
func (f *fakeS3) createUpload(w http.ResponseWriter, r *http.Request, bucket, key string) {
	f.seq++

	id := "zqxupload" + strconv.Itoa(f.seq)
	h, meta := headersOf(r)

	f.uploads[id] = &fakeUpload{
		header: h,
		meta:   meta,
		parts:  map[int][]byte{},
		bucket: bucket,
		key:    key,
	}

	writeXML(w, initiateResult{Bucket: bucket, Key: key, UploadID: id})
}

// uploadPart stores a part. The caller holds f.mu.
func (f *fakeS3) uploadPart(w http.ResponseWriter, r *http.Request, body []byte) {
	u, ok := f.uploads[r.URL.Query().Get("uploadId")]
	if !ok {
		fakeError(w, r, http.StatusNotFound, "NoSuchUpload")

		return
	}

	n, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
	if err != nil {
		fakeError(w, r, http.StatusBadRequest, "InvalidArgument")

		return
	}

	u.parts[n] = body

	w.Header().Set("ETag", `"`+md5Hex(body)+`"`)
	w.WriteHeader(http.StatusOK)
}

// completeRequest is the body of CompleteMultipartUpload.
type completeRequest struct {
	Parts []struct {
		PartNumber int `xml:"PartNumber"`
	} `xml:"Part"`
}

// completeResult is the reply of CompleteMultipartUpload.
type completeResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	Bucket  string   `xml:"Bucket"`
	Key     string   `xml:"Key"`
	ETag    string   `xml:"ETag"`
}

// complete completes an upload in parts, with its condition. The caller
// holds f.mu.
func (f *fakeS3) complete(w http.ResponseWriter, r *http.Request, b *fakeBucket, body []byte) {
	id := r.URL.Query().Get("uploadId")

	u, ok := f.uploads[id]
	if !ok {
		fakeError(w, r, http.StatusNotFound, "NoSuchUpload")

		return
	}

	var req completeRequest

	if xml.Unmarshal(body, &req) != nil || len(req.Parts) == 0 {
		fakeError(w, r, http.StatusBadRequest, "MalformedXML")

		return
	}

	if !condition(w, r, b.objects[u.key]) {
		return
	}

	var data, sums []byte

	for _, p := range req.Parts {
		part, ok := u.parts[p.PartNumber]
		if !ok {
			fakeError(w, r, http.StatusBadRequest, "InvalidPart")

			return
		}

		sum := md5.Sum(part) //nolint:gosec // G401: an ETag.

		data, sums = append(data, part...), append(sums, sum[:]...)
	}

	delete(f.uploads, id)

	etag := md5Hex(sums) + "-" + strconv.Itoa(len(req.Parts))

	f.store(w, b, u.key, &fakeObject{header: u.header, meta: u.meta, etag: etag, data: data})
	writeXML(w, completeResult{Bucket: u.bucket, Key: u.key, ETag: `"` + etag + `"`})
}

// abort aborts an upload in parts. The caller holds f.mu.
func (f *fakeS3) abort(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("uploadId")
	if _, ok := f.uploads[id]; !ok {
		fakeError(w, r, http.StatusNotFound, "NoSuchUpload")

		return
	}

	delete(f.uploads, id)
	w.WriteHeader(http.StatusNoContent)
}

// fakeToken returns the fake's continuation token of cur, which tells the
// key, as a server's may.
func fakeToken(cur listing.Cursor) string {
	flag := "k"
	if cur.Prefix {
		flag = "p"
	}

	return base64.StdEncoding.EncodeToString([]byte(flag + cur.After))
}

// openFakeToken returns the cursor of a token of fakeToken.
func openFakeToken(token string) (listing.Cursor, bool) {
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil || len(b) < 2 || (b[0] != 'k' && b[0] != 'p') {
		return listing.Cursor{}, false
	}

	return listing.Cursor{After: string(b[1:]), Prefix: b[0] == 'p'}, true
}

// listContent is an object of a ListObjectsV2 reply.
type listContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
}

// listPrefix is a common prefix of a ListObjectsV2 reply.
type listPrefix struct {
	Prefix string `xml:"Prefix"`
}

// listResult is the reply of ListObjectsV2.
type listResult struct {
	XMLName               xml.Name      `xml:"ListBucketResult"`
	Name                  string        `xml:"Name"`
	Prefix                string        `xml:"Prefix"`
	NextContinuationToken string        `xml:"NextContinuationToken,omitempty"`
	EncodingType          string        `xml:"EncodingType,omitempty"`
	Contents              []listContent `xml:"Contents"`
	CommonPrefixes        []listPrefix  `xml:"CommonPrefixes"`
	KeyCount              int           `xml:"KeyCount"`
	MaxKeys               int           `xml:"MaxKeys"`
	IsTruncated           bool          `xml:"IsTruncated"`
}

// listObjects answers a ListObjectsV2 of b. The caller holds f.mu.
//
//nolint:funlen // the parameters, the page and its reply.
func (*fakeS3) listObjects(w http.ResponseWriter, r *http.Request, b *fakeBucket) {
	q := r.URL.Query()
	maxKeys := 1000

	if v := q.Get("max-keys"); v != "" {
		maxKeys, _ = strconv.Atoi(v)
	}

	query := listing.Query{
		Prefix:     q.Get("prefix"),
		Delimiter:  q.Get("delimiter"),
		StartAfter: q.Get("start-after"),
		Max:        maxKeys,
	}

	if token := q.Get("continuation-token"); token != "" {
		cur, ok := openFakeToken(token)
		if !ok {
			fakeError(w, r, http.StatusBadRequest, "InvalidArgument")

			return
		}

		query.After = cur
	}

	entries, truncated := listing.Page(slices.Values(slices.Sorted(maps.Keys(b.objects))), query)
	encode := func(s string) string { return s }

	if q.Get("encoding-type") == "url" {
		encode = url.QueryEscape
	}

	res := listResult{
		Name:         "zqxname",
		Prefix:       encode(query.Prefix),
		EncodingType: q.Get("encoding-type"),
		KeyCount:     len(entries),
		MaxKeys:      maxKeys,
		IsTruncated:  truncated,
	}

	for _, e := range entries {
		if e.Prefix {
			res.CommonPrefixes = append(res.CommonPrefixes, listPrefix{Prefix: encode(e.Name)})

			continue
		}

		o := b.objects[e.Name]

		res.Contents = append(res.Contents, listContent{
			Key:          encode(e.Name),
			LastModified: o.modified.UTC().Format(listingTime),
			ETag:         `"` + o.etag + `"`,
			Size:         len(o.data),
		})
	}

	if truncated {
		last := entries[len(entries)-1]

		res.NextContinuationToken = fakeToken(listing.Cursor{After: last.Name, Prefix: last.Prefix})
	}

	writeXML(w, res)
}

// bucketEntry is a bucket of a ListBuckets reply.
type bucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
	BucketRegion string `xml:"BucketRegion"`
}

// bucketsResult is the reply of ListBuckets.
type bucketsResult struct {
	XMLName           xml.Name      `xml:"ListAllMyBucketsResult"`
	ContinuationToken string        `xml:"ContinuationToken,omitempty"`
	Buckets           []bucketEntry `xml:"Buckets>Bucket"`
}

// listBuckets answers a ListBuckets of the buckets testKeyID owns. The
// caller holds f.mu.
func (f *fakeS3) listBuckets(w http.ResponseWriter, r *http.Request) {
	f.record("ListBuckets")

	q := r.URL.Query()
	query := listing.Query{Prefix: q.Get("prefix"), Max: 10000}

	if v := q.Get("max-buckets"); v != "" {
		query.Max, _ = strconv.Atoi(v)
	}

	if token := q.Get("continuation-token"); token != "" {
		cur, ok := openFakeToken(token)
		if !ok {
			fakeError(w, r, http.StatusBadRequest, "InvalidArgument")

			return
		}

		query.After = cur
	}

	var names []string

	for name, b := range f.buckets {
		if b.owner == testKeyID {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	entries, truncated := listing.Page(slices.Values(names), query)

	var res bucketsResult

	for _, e := range entries {
		res.Buckets = append(res.Buckets, bucketEntry{
			Name:         e.Name,
			CreationDate: f.buckets[e.Name].created.UTC().Format(listingTime),
			BucketRegion: testRegion,
		})
	}

	if truncated {
		res.ContinuationToken = fakeToken(listing.Cursor{After: entries[len(entries)-1].Name})
	}

	writeXML(w, res)
}

// rawReply answers every request whose operation is op, as the AWS SDK
// names it in the x-id query parameter or by the method, with status and
// body.
func rawReply(status int, body []byte) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, _ *http.Request) bool {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)

		_, _ = io.Copy(w, bytes.NewReader(body))

		return true
	}
}
