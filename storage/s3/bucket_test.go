package s3

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestProvider_HeadBucket covers HeadBucket against the fake endpoint: the
// request, the response mapping, and how S3's answers map to sentinels.
func TestProvider_HeadBucket(t *testing.T) {
	t.Parallel()

	t.Run("maps the response", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("X-Amz-Bucket-Region", "eu-west-1")
			w.Header().Set("X-Amz-Bucket-Arn", "arn:aws:s3:::b-1")
			w.WriteHeader(http.StatusOK)
		})
		b, err := newTestProvider(t, f).HeadBucket(context.Background(),
			&storage.HeadBucketInput{Bucket: "b-1"})
		if err != nil {
			t.Fatalf("HeadBucket() error: %v", err)
		}
		want := storage.Bucket{Name: "b-1", ID: "arn:aws:s3:::b-1", Region: "eu-west-1"}
		if *b != want {
			t.Errorf("HeadBucket() = %+v, want %+v", b, want)
		}
		req := f.lastRequest(t)
		if req.Method != http.MethodHead || req.Path != "/b-1" {
			t.Errorf("request = %s %s, want HEAD /b-1", req.Method, req.Path)
		}
	})

	// HEAD responses carry no body, so only the status tells these apart.
	for _, tt := range []struct {
		want   error
		name   string
		status int
	}{
		{name: "404 is ErrBucketNotFound", status: http.StatusNotFound, want: storage.ErrBucketNotFound},
		{name: "403 is ErrAccessDenied", status: http.StatusForbidden, want: storage.ErrAccessDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.WriteHeader(tt.status)
			})
			b, err := newTestProvider(t, f).HeadBucket(context.Background(),
				&storage.HeadBucketInput{Bucket: testBucket})
			if !errors.Is(err, tt.want) || b != nil {
				t.Errorf("HeadBucket() = %+v, %v; want nil, %v", b, err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: head bucket: ") {
				t.Errorf("error = %q, want the head bucket prefix", err)
			}
		})
	}

	t.Run("invalid name sends nothing", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		_, err := newTestProvider(t, f).HeadBucket(context.Background(),
			&storage.HeadBucketInput{Bucket: ""})
		if !errors.Is(err, storage.ErrInvalidBucket) || len(f.requests()) != 0 {
			t.Errorf("HeadBucket(\"\") = %v with %d requests; want ErrInvalidBucket, none",
				err, len(f.requests()))
		}
	})
}

// TestProvider_DeleteBucket covers DeleteBucket against the fake endpoint.
func TestProvider_DeleteBucket(t *testing.T) {
	t.Parallel()

	t.Run("sends DELETE on the bucket", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNoContent)
		})
		if err := newTestProvider(t, f).DeleteBucket(context.Background(),
			&storage.DeleteBucketInput{Bucket: "b-1"}); err != nil {
			t.Fatalf("DeleteBucket() error: %v", err)
		}
		req := f.lastRequest(t)
		if req.Method != http.MethodDelete || req.Path != "/b-1" {
			t.Errorf("request = %s %s, want DELETE /b-1", req.Method, req.Path)
		}
	})

	for _, tt := range []struct {
		want   error
		name   string
		code   string
		status int
	}{
		{name: "NoSuchBucket", status: http.StatusNotFound, code: "NoSuchBucket", want: storage.ErrBucketNotFound},
		{name: "BucketNotEmpty", status: http.StatusConflict, code: "BucketNotEmpty", want: storage.ErrBucketNotEmpty},
		{name: "AccessDenied", status: http.StatusForbidden, code: "AccessDenied", want: storage.ErrAccessDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				writeS3Error(w, tt.status, tt.code)
			})
			err := newTestProvider(t, f).DeleteBucket(context.Background(),
				&storage.DeleteBucketInput{Bucket: testBucket})
			if !errors.Is(err, tt.want) {
				t.Errorf("DeleteBucket() = %v, want %v", err, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: delete bucket: ") {
				t.Errorf("error = %q, want the delete bucket prefix", err)
			}
		})
	}

	t.Run("invalid name sends nothing", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		err := newTestProvider(t, f).DeleteBucket(context.Background(),
			&storage.DeleteBucketInput{Bucket: "Bad_Bucket"})
		if !errors.Is(err, storage.ErrInvalidBucket) || len(f.requests()) != 0 {
			t.Errorf("DeleteBucket(Bad_Bucket) = %v with %d requests; want ErrInvalidBucket, none",
				err, len(f.requests()))
		}
	})
}

// TestProvider_BucketMethodsZeroValue checks that HeadBucket and
// DeleteBucket of a Provider not built by NewProvider, including a nil
// pointer, return ErrNotInitialized.
func TestProvider_BucketMethodsZeroValue(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]*Provider{"nil pointer": nil, "zero": {}} {
		_, err := p.HeadBucket(context.Background(), &storage.HeadBucketInput{Bucket: testBucket})
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: HeadBucket() error = %v, want ErrNotInitialized", name, err)
		}
		err = p.DeleteBucket(context.Background(), &storage.DeleteBucketInput{Bucket: testBucket})
		if !errors.Is(err, storage.ErrNotInitialized) {
			t.Errorf("%s: DeleteBucket() error = %v, want ErrNotInitialized", name, err)
		}
	}
}
