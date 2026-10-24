package projectknownissues_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// Docs: project/known-issues.mdx#tests-only
// "One case of `TestProvider_PutObjectContentLength` in `storage/s3`"
func TestKI010TestExists(t *testing.T) {
	t.Parallel()
	src := readFile(t, filepath.Join(repoRoot(t), "storage", "s3", "provider_test.go"))
	if !strings.Contains(src, "func TestProvider_PutObjectContentLength(") {
		t.Fatalf("storage/s3/provider_test.go has no TestProvider_PutObjectContentLength")
	}
}

// Docs: project/known-issues.mdx#tests-only
// "The runtime behavior is correct; only the test assertion is fragile."
// Checked as: a multipart upload whose body is shorter than its
// ContentLength fails with ErrContentLengthMismatch against a fake S3 that
// accepts every request.
func TestKI010MultipartShortBodyMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult>` +
				`<Bucket>bkt</Bucket><Key>big</Key><UploadId>UP1</UploadId></InitiateMultipartUploadResult>`))
		case r.Method == http.MethodPut && q.Has("partNumber"):
			w.Header().Set("ETag", `"part"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult>` +
				`<Bucket>bkt</Bucket><Key>big</Key><ETag>"x-2"</ETag></CompleteMultipartUploadResult>`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"storage": map[string]any{
		"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s", "endpoint": srv.URL, "bucket": "bkt",
	}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s3svc.NewProvider()(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 20<<20)
	n := int64(30 << 20)
	_, err = p.PutObject(t.Context(), &storage.PutObjectInput{
		Body: io.MultiReader(bytes.NewReader(body)), ContentLength: &n,
		BaseObjectInput: storage.BaseObjectInput{Key: "big"},
	})
	if !errors.Is(err, storage.ErrContentLengthMismatch) {
		t.Fatalf("multipart PutObject with a short body error = %v, want ErrContentLengthMismatch", err)
	}
}
