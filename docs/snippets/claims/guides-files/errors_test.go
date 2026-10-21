package guidesfiles_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidKey`, `ErrInvalidBucket`, `ErrInputRequired` | Input rejected
// before anything is sent"
func TestInputRejectedBeforeSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	badKey := storage.BaseObjectInput{Key: ""}
	badBucket := storage.BaseObjectInput{Bucket: ptr("Bad_Bucket!"), Key: "k"}
	checks := func(p storage.Provider) map[string]struct {
		err  error
		want error
	} {
		run := func(f func() error) error { return f() }
		return map[string]struct {
			err  error
			want error
		}{
			"PutObject empty key": {run(func() error {
				_, err := p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: badKey})
				return err
			}), storage.ErrInvalidKey},
			"GetObject empty key": {run(func() error {
				_, err := p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: badKey}})
				return err
			}), storage.ErrInvalidKey},
			"HeadObject invalid bucket": {run(func() error {
				_, err := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: badBucket})
				return err
			}), storage.ErrInvalidBucket},
			"CreateBucket invalid bucket": {run(func() error {
				_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "Bad_Bucket!"})
				return err
			}), storage.ErrInvalidBucket},
			"PutObject nil input": {run(func() error {
				_, err := p.PutObject(ctx, nil)
				return err
			}), storage.ErrInputRequired},
			"ListObjects nil input": {run(func() error {
				_, err := p.ListObjects(ctx, nil)
				return err
			}), storage.ErrInputRequired},
			"HeadBucket nil input": {run(func() error {
				_, err := p.HeadBucket(ctx, nil)
				return err
			}), storage.ErrInputRequired},
		}
	}

	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		for name, c := range checks(f.provider(t, "us-east-1", "bkt")) {
			wantErr(t, name, c.err, c.want)
		}
		pre := f.presigner(t, "us-east-1", "bkt")
		_, err := pre.PresignUploadURL(ctx, nil)
		wantErr(t, "PresignUploadURL nil input", err, storage.ErrInputRequired)
		_, err = pre.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{})
		wantErr(t, "PresignDownloadURL empty key", err, storage.ErrInvalidKey)
		if n := f.requests.Load(); n != 0 {
			t.Fatalf("rejected inputs sent %d requests, want 0", n)
		}
	})
	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		// No bucket exists, so a check made after a disk lookup would report
		// ErrBucketNotFound instead.
		p, _ := newDirProvider(t, "bkt")
		for name, c := range checks(p) {
			wantErr(t, name, c.err, c.want)
		}
	})
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidOption` | A constructor given a nil option"
func TestNilOption(t *testing.T) {
	t.Parallel()
	cfg := mockConfig(t, map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s"})
	_, err := s3svc.NewProvider(nil)(cfg)
	wantErr(t, "s3.NewProvider(nil)", err, storage.ErrInvalidOption)
	_, err = s3svc.NewPresigner(nil)(cfg)
	wantErr(t, "s3.NewPresigner(nil)", err, storage.ErrInvalidOption)
	_, err = storagedir.NewProvider(nil)(cfg)
	wantErr(t, "dir.NewProvider(nil)", err, storage.ErrInvalidOption)
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrNotInitialized` | A provider not built by its constructor"
func TestNotInitialized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	o := obj("k")
	for name, p := range map[string]storage.Provider{"s3": &s3svc.Provider{}, "dir": &storagedir.Provider{}} {
		calls := map[string]error{}
		_, calls["CreateBucket"] = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "bkt"})
		_, calls["HeadBucket"] = p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "bkt"})
		calls["DeleteBucket"] = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "bkt"})
		_, calls["ListBuckets"] = p.ListBuckets(ctx, &storage.ListBucketsInput{})
		_, calls["HeadObject"] = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: o})
		_, calls["PutObject"] = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: o})
		_, calls["GetObject"] = p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: o}})
		_, calls["DeleteObject"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: o})
		_, calls["ListObjects"] = p.ListObjects(ctx, &storage.ListObjectsInput{})
		for m, err := range calls {
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("zero %s %s error = %v, want ErrNotInitialized", name, m, err)
			}
		}
	}
	pre := &s3svc.Presigner{}
	_, err := pre.PresignUploadURL(ctx, &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{BaseObjectInput: o}})
	wantErr(t, "zero s3.Presigner PresignUploadURL", err, storage.ErrNotInitialized)
	_, err = pre.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: o}},
	})
	wantErr(t, "zero s3.Presigner PresignDownloadURL", err, storage.ErrNotInitialized)
}
