package s3

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// listResponse writes a ListObjectsV2 response body with the given inner
// XML.
func listResponse(w http.ResponseWriter, inner string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>` +
		inner + `</ListBucketResult>`))
}

// TestProvider_ListObjects_Request checks that ListObjects sends every
// selection field, the resolved bucket, and encoding-type url.
func TestProvider_ListObjects_Request(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		listResponse(w, "<IsTruncated>false</IsTruncated>")
	})
	p := newTestProvider(t, f)

	_, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{
		ContinuationToken: new("tok"),
		Delimiter:         new("/"),
		MaxKeys:           new(int32(7)),
		Prefix:            new("p/"),
		StartAfter:        new("p/a"),
	})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}

	req := f.lastRequest(t)
	if req.Method != http.MethodGet || req.Path != "/"+testBucket {
		t.Errorf("request = %s %s, want GET /%s", req.Method, req.Path, testBucket)
	}
	want := map[string]string{
		"list-type":          "2",
		"continuation-token": "tok",
		"delimiter":          "/",
		"max-keys":           "7",
		"prefix":             "p/",
		"start-after":        "p/a",
		"encoding-type":      "url",
	}
	for k, v := range want {
		if got := req.Query[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %q, want %q", k, got, v)
		}
	}
}

// TestProvider_ListObjects_Decode checks that keys and common prefixes are
// URL-decoded and the page fields are carried over.
func TestProvider_ListObjects_Decode(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		listResponse(w, "<IsTruncated>true</IsTruncated>"+
			"<NextContinuationToken>next</NextContinuationToken>"+
			"<Contents><Key>a+b%2Bc%01%E6%97%A5</Key>"+
			"<LastModified>2026-03-04T05:06:07.000Z</LastModified>"+
			"<ETag>&quot;abc&quot;</ETag><Size>3</Size></Contents>"+
			"<CommonPrefixes><Prefix>d%20e/</Prefix></CommonPrefixes>")
	})
	p := newTestProvider(t, f)

	out, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	want := storage.ObjectSummary{
		Key:          "a b+c\x01日",
		ETag:         "abc",
		Size:         3,
		LastModified: lastModified,
	}
	if len(out.Objects) != 1 || out.Objects[0] != want {
		t.Errorf("Objects = %+v, want [%+v]", out.Objects, want)
	}
	if len(out.CommonPrefixes) != 1 || out.CommonPrefixes[0] != "d e/" {
		t.Errorf("CommonPrefixes = %q, want [\"d e/\"]", out.CommonPrefixes)
	}
	if !out.IsTruncated || out.NextContinuationToken != "next" {
		t.Errorf("IsTruncated, NextContinuationToken = %v, %q; want true, next",
			out.IsTruncated, out.NextContinuationToken)
	}
}

// TestProvider_ListObjects_BadEncoding checks that a key S3 encoded badly
// fails the call without quoting the key.
func TestProvider_ListObjects_BadEncoding(t *testing.T) {
	t.Parallel()
	for name, inner := range map[string]string{
		"key":           "<Contents><Key>secret%zz</Key></Contents>",
		"common prefix": "<CommonPrefixes><Prefix>secret%zz/</Prefix></CommonPrefixes>",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				listResponse(w, inner)
			})
			_, err := newTestProvider(t, f).ListObjects(context.Background(),
				&storage.ListObjectsInput{})
			if err == nil || strings.Contains(err.Error(), "secret") ||
				strings.Contains(err.Error(), "%zz") {
				t.Errorf("ListObjects() error = %v, want an error that quotes no key", err)
			}
		})
	}
}

// TestProvider_ListObjects_Errors checks that a missing bucket maps to
// ErrBucketNotFound and that invalid inputs send no request.
func TestProvider_ListObjects_Errors(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket")
	})
	p := newTestProvider(t, f)
	ctx := context.Background()

	_, err := p.ListObjects(ctx, &storage.ListObjectsInput{})
	assertOnlySentinel(t, err, storage.ErrBucketNotFound)
	if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: list: ") {
		t.Errorf("error = %q, want the list prefix", err)
	}

	sent := len(f.requests())
	invalid := map[error]*storage.ListObjectsInput{
		storage.ErrInvalidBucket:            {Bucket: new("")},
		storage.ErrInvalidLimit:             {MaxKeys: new(int32(0))},
		storage.ErrInvalidContinuationToken: {ContinuationToken: new("")},
	}
	for want, in := range invalid {
		if _, err := p.ListObjects(ctx, in); !errors.Is(err, want) {
			t.Errorf("ListObjects(%+v) error = %v, want %v", in, err, want)
		}
	}
	if n := len(f.requests()); n != sent {
		t.Errorf("invalid inputs sent %d requests, want none", n-sent)
	}
}

// TestProvider_ListBuckets checks the request ListBuckets sends and the page
// it returns.
func TestProvider_ListBuckets(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult>` +
			`<Buckets><Bucket><Name>b-1</Name>` +
			`<CreationDate>2026-01-02T03:04:05.000Z</CreationDate>` +
			`<BucketArn>arn:aws:s3:::b-1</BucketArn><BucketRegion>eu-west-1</BucketRegion>` +
			`</Bucket></Buckets>` +
			`<ContinuationToken>more</ContinuationToken></ListAllMyBucketsResult>`))
	})
	p := newTestProvider(t, f)

	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{
		ContinuationToken: new("tok"),
		MaxBuckets:        new(int32(3)),
		Prefix:            new("b-"),
	})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	want := storage.Bucket{
		Name: "b-1", CreationDate: created, ID: "arn:aws:s3:::b-1", Region: "eu-west-1",
	}
	if len(out.Buckets) != 1 || out.Buckets[0] != want || out.NextContinuationToken != "more" {
		t.Errorf("ListBuckets() = %+v, want [%+v] and token more", out, want)
	}

	req := f.lastRequest(t)
	if req.Method != http.MethodGet || req.Path != "/" {
		t.Errorf("request = %s %s, want GET /", req.Method, req.Path)
	}
	for k, v := range map[string]string{
		"continuation-token": "tok",
		"max-buckets":        "3",
		"prefix":             "b-",
	} {
		if got := req.Query[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %q, want %q", k, got, v)
		}
	}

	sent := len(f.requests())
	for _, n := range []int32{0, 10001} {
		_, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{MaxBuckets: &n})
		if !errors.Is(err, storage.ErrInvalidLimit) {
			t.Errorf("ListBuckets(MaxBuckets %d) error = %v, want ErrInvalidLimit", n, err)
		}
	}
	if n := len(f.requests()); n != sent {
		t.Errorf("invalid inputs sent %d requests, want none", n-sent)
	}
}

// TestProvider_ZeroValue_List checks that the list methods of a Provider not
// built by NewProvider, including a nil pointer, return ErrNotInitialized
// before they look at the input, and send nothing.
func TestProvider_ZeroValue_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for name, p := range map[string]*Provider{"nil pointer": nil, "zero": {}} {
		_, err := p.ListObjects(ctx, &storage.ListObjectsInput{})
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: ListObjects() error = %v, want ErrNotInitialized", name, err)
		}
		_, err = p.ListObjects(ctx, nil)
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: ListObjects(nil) error = %v, want ErrNotInitialized", name, err)
		}
		_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: ListBuckets() error = %v, want ErrNotInitialized", name, err)
		}
		_, err = p.ListBuckets(ctx, nil)
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: ListBuckets(nil) error = %v, want ErrNotInitialized", name, err)
		}
	}
}
