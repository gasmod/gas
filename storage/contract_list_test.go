package storage_test

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// memS3 emulates ListObjectsV2 and ListBuckets independently of the dir
// backend: it walks the keys in order and stops at the page size, where dir
// collects every entry and sorts. It always answers with encoding-type url,
// which the s3 backend always requests, and issues its own opaque tokens.

// memTimeFormat is the ISO 8601 form S3 XML responses carry times in.
const memTimeFormat = "2006-01-02T15:04:05.000Z"

// memToken returns the continuation token memS3 issues to resume after name.
func memToken(name string) string {
	return "mem:" + base64.StdEncoding.EncodeToString([]byte(name))
}

// memUntoken returns the name a memS3 token resumes after.
func memUntoken(tok string) (string, bool) {
	enc, ok := strings.CutPrefix(tok, "mem:")
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	return string(b), err == nil
}

// xmlText escapes s for an XML text node.
func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// memListObjects serves ListObjectsV2 on objs: keys under prefix after
// start-after, rolled up at the delimiter, after the continuation token, at
// most max-keys of them.
func memListObjects(w http.ResponseWriter, r *http.Request, bucket string,
	objs map[string]*memObject,
) {
	q := r.URL.Query()
	prefix, delim, startAfter := q.Get("prefix"), q.Get("delimiter"), q.Get("start-after")
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			memError(w, r, http.StatusBadRequest, "InvalidArgument")
			return
		}
		maxKeys = min(n, 1000)
	}
	after := ""
	if q.Has("continuation-token") {
		var ok bool
		if after, ok = memUntoken(q.Get("continuation-token")); !ok {
			memError(w, r, http.StatusBadRequest, "InvalidArgument")
			return
		}
	}

	var contents, prefixes strings.Builder
	var last string
	count, truncated := 0, false
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range objs {
			if !yield(k) {
				return
			}
		}
	}) {
		if !strings.HasPrefix(k, prefix) || k <= startAfter {
			continue
		}
		name, isPrefix := k, false
		if i := strings.Index(k[len(prefix):], delim); delim != "" && i >= 0 {
			name, isPrefix = k[:len(prefix)+i+len(delim)], true
		}
		if name <= after || (count > 0 && name == last) {
			continue
		}
		if count == maxKeys {
			truncated = true
			break
		}
		count++
		last = name
		if isPrefix {
			_, _ = fmt.Fprintf(&prefixes, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>",
				url.QueryEscape(name))
			continue
		}
		o := objs[k]
		_, _ = fmt.Fprintf(&contents,
			"<Contents><Key>%s</Key><LastModified>%s</LastModified>"+
				"<ETag>&quot;%s&quot;</ETag><Size>%d</Size></Contents>",
			url.QueryEscape(k), o.modified.Format(memTimeFormat), o.version, len(o.body))
	}

	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<ListBucketResult><Name>%s</Name><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys>`+
		`<EncodingType>url</EncodingType><IsTruncated>%t</IsTruncated>%s%s`,
		xmlText(bucket), count, maxKeys, truncated, contents.String(), prefixes.String())
	if truncated {
		_, _ = fmt.Fprintf(w, "<NextContinuationToken>%s</NextContinuationToken>",
			xmlText(memToken(last)))
	}
	_, _ = fmt.Fprint(w, "</ListBucketResult>")
}

// listBuckets serves ListBuckets for the signing account: its buckets under
// prefix, after the continuation token, at most max-buckets of them. The
// caller holds m.mu.
func (m *memS3) listBuckets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	caller := requestAccessKey(r)
	maxBuckets := 10000
	if v := q.Get("max-buckets"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10000 {
			memError(w, r, http.StatusBadRequest, "InvalidArgument")
			return
		}
		maxBuckets = n
	}
	after := ""
	if q.Has("continuation-token") {
		var ok bool
		if after, ok = memUntoken(q.Get("continuation-token")); !ok {
			memError(w, r, http.StatusBadRequest, "InvalidArgument")
			return
		}
	}

	var names []string
	for name, owner := range m.owners {
		if owner == caller && strings.HasPrefix(name, q.Get("prefix")) && name > after {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	truncated := len(names) > maxBuckets
	if truncated {
		names = names[:maxBuckets]
	}

	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult><Buckets>`)
	for _, name := range names {
		_, _ = fmt.Fprintf(w, "<Bucket><Name>%s</Name><CreationDate>%s</CreationDate></Bucket>",
			xmlText(name), memBucketCreated.Format(memTimeFormat))
	}
	_, _ = fmt.Fprint(w, "</Buckets>")
	if truncated {
		_, _ = fmt.Fprintf(w, "<ContinuationToken>%s</ContinuationToken>",
			xmlText(memToken(names[len(names)-1])))
	}
	_, _ = fmt.Fprint(w, "</ListAllMyBucketsResult>")
}

// memBucketCreated is the creation date memS3 reports for every bucket.
var memBucketCreated = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// listKeys are the keys the list contract stores: nested and flat, a key
// ending in the delimiter, and keys with bytes that need encoding in XML or
// a URL.
var listKeys = []string{
	"a", "b/1", "b/2", "b/c/3", "c d", "c+d", "c%2Fd", "e\x01f", "z/", "日本",
}

// listPage is what one or more ListObjects pages held, joined.
type listPage struct {
	objects  []string
	prefixes []string
	pages    int
}

// listAll walks every page of in, starting from its ContinuationToken, and
// returns the objects and common prefixes in order. It fails the test when
// an object's Size is not that of its body ("k" + key) or a page breaks the
// truncation contract.
func listAll(t *testing.T, p storage.Provider, in storage.ListObjectsInput) listPage {
	t.Helper()
	var got listPage
	for {
		out, err := p.ListObjects(context.Background(), &in)
		if err != nil {
			t.Fatalf("ListObjects(page %d) error: %v", got.pages, err)
		}
		got.pages++
		for _, o := range out.Objects {
			got.objects = append(got.objects, o.Key)
			if o.Size != int64(len("k"+o.Key)) || o.LastModified.IsZero() {
				t.Errorf("ListObjects() object %q = %+v, want Size %d and LastModified set",
					o.Key, o, len("k"+o.Key))
			}
		}
		got.prefixes = append(got.prefixes, out.CommonPrefixes...)
		if !out.IsTruncated {
			if out.NextContinuationToken != "" {
				t.Errorf("last page NextContinuationToken = %q, want empty",
					out.NextContinuationToken)
			}
			return got
		}
		if out.NextContinuationToken == "" {
			t.Fatal("truncated page has no NextContinuationToken")
		}
		if got.pages > len(listKeys)+1 {
			t.Fatal("ListObjects() does not end")
		}
		in.ContinuationToken = new(out.NextContinuationToken)
	}
}

// TestContract_List checks that both backends list the same objects, common
// prefixes and buckets, and page through them alike.
func TestContract_List(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T, p storage.Provider){
		"list objects":        contractListObjects,
		"list empty":          contractListEmpty,
		"list validation":     contractListValidation,
		"list buckets":        contractListBuckets,
		"list after a delete": contractListAfterDelete,
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

// contractListObjects stores listKeys and lists them with each combination
// of the selection fields, in one page and in pages of two.
func contractListObjects(t *testing.T, p storage.Provider) {
	for _, k := range listKeys {
		if _, err := cput(p, key(k), strings.NewReader("k"+k), nil, nil); err != nil {
			t.Fatalf("PutObject(%q) error: %v", k, err)
		}
	}

	sorted := slices.Sorted(slices.Values(listKeys))
	tests := []struct {
		in       storage.ListObjectsInput
		name     string
		objects  []string
		prefixes []string
	}{
		{name: "everything", objects: sorted},
		{
			name:     "delimiter",
			in:       storage.ListObjectsInput{Delimiter: new("/")},
			objects:  []string{"a", "c d", "c%2Fd", "c+d", "e\x01f", "日本"},
			prefixes: []string{"b/", "z/"},
		},
		{
			name:     "prefix and delimiter",
			in:       storage.ListObjectsInput{Prefix: new("b/"), Delimiter: new("/")},
			objects:  []string{"b/1", "b/2"},
			prefixes: []string{"b/c/"},
		},
		{
			name:    "prefix",
			in:      storage.ListObjectsInput{Prefix: new("c")},
			objects: []string{"c d", "c%2Fd", "c+d"},
		},
		{
			name:    "start after",
			in:      storage.ListObjectsInput{StartAfter: new("b/2")},
			objects: []string{"b/c/3", "c d", "c%2Fd", "c+d", "e\x01f", "z/", "日本"},
		},
		{
			name:    "start after a missing key",
			in:      storage.ListObjectsInput{StartAfter: new("c")},
			objects: []string{"c d", "c%2Fd", "c+d", "e\x01f", "z/", "日本"},
		},
		{
			// A key after StartAfter still rolls up into its common prefix,
			// even when the prefix itself sorts before StartAfter, as
			// LocalStack does.
			name:     "start after inside a common prefix",
			in:       storage.ListObjectsInput{StartAfter: new("b/1"), Delimiter: new("/")},
			objects:  []string{"c d", "c%2Fd", "c+d", "e\x01f", "日本"},
			prefixes: []string{"b/", "z/"},
		},
		{
			name:     "start after the last key of a common prefix",
			in:       storage.ListObjectsInput{StartAfter: new("b/c/3"), Delimiter: new("/")},
			objects:  []string{"c d", "c%2Fd", "c+d", "e\x01f", "日本"},
			prefixes: []string{"z/"},
		},
		{
			name:     "multi-byte delimiter",
			in:       storage.ListObjectsInput{Delimiter: new("c/")},
			objects:  []string{"a", "b/1", "b/2", "c d", "c%2Fd", "c+d", "e\x01f", "z/", "日本"},
			prefixes: []string{"b/c/"},
		},
		{name: "prefix matching nothing", in: storage.ListObjectsInput{Prefix: new("q")}},
	}
	for _, tt := range tests {
		for _, maxKeys := range []*int32{nil, new(int32(2)), new(int32(1))} {
			in := tt.in
			in.MaxKeys = maxKeys
			got := listAll(t, p, in)
			if !slices.Equal(got.objects, tt.objects) || !slices.Equal(got.prefixes, tt.prefixes) {
				t.Errorf("%s (MaxKeys %v): objects %q, prefixes %q; want %q, %q",
					tt.name, maxKeysString(maxKeys), got.objects, got.prefixes,
					tt.objects, tt.prefixes)
			}
			entries := len(tt.objects) + len(tt.prefixes)
			if maxKeys != nil && entries > 0 {
				if want := (entries + int(*maxKeys) - 1) / int(*maxKeys); got.pages != want {
					t.Errorf("%s (MaxKeys %d): %d pages, want %d",
						tt.name, *maxKeys, got.pages, want)
				}
			}
		}
	}
}

// maxKeysString formats an optional MaxKeys for a test message.
func maxKeysString(n *int32) string {
	if n == nil {
		return "nil"
	}
	return strconv.Itoa(int(*n))
}

// contractListEmpty lists a bucket with no objects: one empty page.
func contractListEmpty(t *testing.T, p storage.Provider) {
	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	if len(out.Objects) != 0 || len(out.CommonPrefixes) != 0 || out.IsTruncated ||
		out.NextContinuationToken != "" {
		t.Errorf("ListObjects() of an empty bucket = %+v, want an empty last page", out)
	}
}

// contractListValidation checks the inputs both backends reject, and that a
// missing bucket is ErrBucketNotFound.
func contractListValidation(t *testing.T, p storage.Provider) {
	ctx := context.Background()
	objects := map[string]*storage.ListObjectsInput{
		"ErrInputRequired":            nil,
		"ErrInvalidBucket":            {Bucket: new("Bad_Bucket")},
		"ErrBucketNotFound":           {Bucket: new("missing-bucket")},
		"ErrInvalidLimit":             {MaxKeys: new(int32(0))},
		"ErrInvalidContinuationToken": {ContinuationToken: new("")},
	}
	for want, in := range objects {
		_, err := p.ListObjects(ctx, in)
		if got := classify(err); got != want {
			t.Errorf("ListObjects(%+v) = %s (%v), want %s", in, got, err, want)
		}
	}
	if _, err := p.ListObjects(ctx, &storage.ListObjectsInput{MaxKeys: new(int32(-1))}); classify(
		err) != "ErrInvalidLimit" {
		t.Errorf("ListObjects(MaxKeys -1) = %s (%v), want ErrInvalidLimit", classify(err), err)
	}

	buckets := map[string]*storage.ListBucketsInput{
		"ErrInputRequired":            nil,
		"ErrInvalidLimit":             {MaxBuckets: new(int32(0))},
		"ErrInvalidContinuationToken": {ContinuationToken: new("")},
	}
	for want, in := range buckets {
		_, err := p.ListBuckets(ctx, in)
		if got := classify(err); got != want {
			t.Errorf("ListBuckets(%+v) = %s (%v), want %s", in, got, err, want)
		}
	}
	if _, err := p.ListBuckets(
		ctx,
		&storage.ListBucketsInput{MaxBuckets: new(int32(10001))},
	); classify(
		err,
	) != "ErrInvalidLimit" {
		t.Errorf("ListBuckets(MaxBuckets 10001) = %s (%v), want ErrInvalidLimit",
			classify(err), err)
	}
}

// contractListBuckets creates buckets and lists them by prefix, in one page
// and one bucket at a time.
func contractListBuckets(t *testing.T, p storage.Provider) {
	ctx := context.Background()
	for _, b := range []string{"list-ab", "list-aa", "list-b"} {
		if _, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: b}); err != nil {
			t.Fatalf("CreateBucket(%s) error: %v", b, err)
		}
	}

	out, err := p.ListBuckets(ctx, &storage.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	all := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		all = append(all, b.Name)
		if b.CreationDate.IsZero() {
			t.Errorf("bucket %s has no CreationDate", b.Name)
		}
	}
	if want := []string{contractBucket, "list-aa", "list-ab", "list-b"}; !slices.Equal(all, want) ||
		out.ContinuationToken != "" {
		t.Errorf("ListBuckets() = %q, token %q; want %q in one page", all, out.ContinuationToken,
			want)
	}

	in := storage.ListBucketsInput{Prefix: new("list-a"), MaxBuckets: new(int32(1))}
	var paged []string
	for range 4 {
		out, err := p.ListBuckets(ctx, &in)
		if err != nil {
			t.Fatalf("ListBuckets(%+v) error: %v", in, err)
		}
		for _, b := range out.Buckets {
			paged = append(paged, b.Name)
		}
		if out.ContinuationToken == "" {
			break
		}
		in.ContinuationToken = new(out.ContinuationToken)
	}
	if want := []string{"list-aa", "list-ab"}; !slices.Equal(paged, want) {
		t.Errorf("ListBuckets(prefix list-a, one per page) = %q, want %q", paged, want)
	}
}

// contractListAfterDelete checks that a deleted object is not listed and an
// overwritten one is listed once, with its new size.
func contractListAfterDelete(t *testing.T, p storage.Provider) {
	for _, k := range []string{"gone", "kept"} {
		if _, err := cput(p, key(k), strings.NewReader("k"+k), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cdel(p, key("gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := cput(p, key("kept"), strings.NewReader("kkept"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := listAll(t, p, storage.ListObjectsInput{}); !slices.Equal(got.objects,
		[]string{"kept"}) {
		t.Errorf("ListObjects() after delete and overwrite = %q, want [kept]", got.objects)
	}
}
