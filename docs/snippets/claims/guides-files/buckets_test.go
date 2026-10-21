package guidesfiles_test

import (
	"net/http"
	"strings"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gasmod/gas/storage"
)

// enableVersioning turns on versioning for b's bucket through the raw client
// the s3 Provider exposes.
func enableVersioning(t *testing.T, b backend) {
	t.Helper()
	if _, err := b.s3.Client().PutBucketVersioning(t.Context(), &awss3.PutBucketVersioningInput{
		Bucket:                  &b.bucket,
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatalf("PutBucketVersioning() error: %v", err)
	}
}

// Docs: guides/files.mdx#operations
// "`CreateBucket(ctx, *CreateBucketInput)` | `*Bucket` | A bucket you already
// own is success"
func TestCreateOwnedBucketSucceeds(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "kept", "x")
			got, err := b.p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: b.bucket})
			if err != nil {
				t.Fatalf("CreateBucket(owned) error = %v, want nil", err)
			}
			if got == nil || got.Name != b.bucket {
				t.Fatalf("CreateBucket(owned) = %+v, want Name %q", got, b.bucket)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "For a bucket you already own it succeeds, and the returned `Bucket` may
// hold only the name."
// Run outside us-east-1, where S3 answers BucketAlreadyOwnedByYou.
func TestCreateOwnedBucketOutsideUSEast1(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("owned")
	p := newS3Provider(t, "eu-west-1", bucket)
	mustCreateBucket(t, p, bucket)
	got, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("CreateBucket(owned, eu-west-1) error = %v, want nil", err)
	}
	if got == nil || got.Name != bucket {
		t.Fatalf("CreateBucket(owned, eu-west-1) = %+v, want Name %q", got, bucket)
	}
}

// Docs: guides/files.mdx#operations
// "`HeadBucket(ctx, *HeadBucketInput)` | `*Bucket` | Whether a bucket exists,
// and what the backend knows about it"
func TestHeadBucket(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			got, err := b.p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: b.bucket})
			if err != nil {
				t.Fatalf("HeadBucket(existing) error: %v", err)
			}
			if got == nil || got.Name != b.bucket {
				t.Fatalf("HeadBucket(existing) = %+v, want Name %q", got, b.bucket)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`DeleteBucket(ctx, *DeleteBucketInput)` | `error` | Deletes an empty
// bucket; a missing one is `ErrBucketNotFound`"
// Docs: guides/files.mdx#errors-and-testing
// "`ErrBucketNotEmpty` | `DeleteBucket` of a bucket that holds objects"
func TestDeleteBucket(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "k", "x")
			err := b.p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: b.bucket})
			wantErr(t, "DeleteBucket(non-empty)", err, storage.ErrBucketNotEmpty)
			if got := readObject(t, b.p, "k"); got != "x" {
				t.Fatalf("object after refused DeleteBucket = %q, want kept", got)
			}
			if _, err := b.p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("k")}); err != nil {
				t.Fatalf("DeleteObject() error: %v", err)
			}
			if err := b.p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: b.bucket}); err != nil {
				t.Fatalf("DeleteBucket(empty) error = %v, want nil", err)
			}
			_, err = b.p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: b.bucket})
			wantErr(t, "HeadBucket(deleted)", err, storage.ErrBucketNotFound)
			err = b.p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: b.bucket})
			wantErr(t, "DeleteBucket(missing)", err, storage.ErrBucketNotFound)
		})
	}
}

// Docs: guides/files.mdx#operations
// "On S3, `CreateBucket` creates the bucket in the client's region and sends
// the location constraint S3 requires outside us-east-1 and `aws-global`."
func TestCreateBucketLocationConstraint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		region     string
		constraint bool
	}{
		{"us-east-1", false},
		{"aws-global", false},
		{"eu-west-1", true},
		{"ap-southeast-2", true},
	} {
		t.Run(tc.region, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p := f.provider(t, tc.region, "")
			if _, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "loc-bucket"}); err != nil {
				t.Fatalf("CreateBucket() error: %v", err)
			}
			f.mu.Lock()
			body := strings.Join(f.bodies, "")
			f.mu.Unlock()
			has := strings.Contains(body, "<LocationConstraint>"+tc.region+"</LocationConstraint>")
			if has != tc.constraint {
				t.Fatalf("region %s: request body %q, location constraint sent = %v, want %v", tc.region, body, has, tc.constraint)
			}
			if !tc.constraint && strings.Contains(body, "LocationConstraint") {
				t.Fatalf("region %s: request body %q carries a location constraint, want none", tc.region, body)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "On S3, `CreateBucket` creates the bucket in the client's region"
func TestCreateBucketInClientRegion(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("region")
	p := newS3Provider(t, "eu-west-1", "")
	mustCreateBucket(t, p, bucket)
	got, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("HeadBucket() error: %v", err)
	}
	if got.Region != "eu-west-1" {
		t.Fatalf("bucket Region = %q, want eu-west-1", got.Region)
	}
}

// Docs: guides/files.mdx#operations
// "With no configured region it uses the region the AWS SDK resolved."
// Docs: guides/files.mdx#config
// "`storage.region` | AWS region (s3). Empty uses the AWS SDK's default
// resolution"
func TestCreateBucketSDKResolvedRegion(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_REGION", "eu-central-1")
	f := newFakeS3(t, nil)
	p := f.provider(t, "", "")
	if _, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "sdk-region"}); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	f.mu.Lock()
	body := strings.Join(f.bodies, "")
	f.mu.Unlock()
	if !strings.Contains(body, "<LocationConstraint>eu-central-1</LocationConstraint>") {
		t.Fatalf("CreateBucket request body = %q, want the SDK-resolved region eu-central-1 as location constraint", body)
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrBucketExists` | S3 `CreateBucket` of a name another account owns"
func TestBucketExists(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusConflict, "BucketAlreadyExists")
	})
	_, err := f.provider(t, "us-east-1", "").CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "taken"})
	wantErr(t, "CreateBucket(taken by another account)", err, storage.ErrBucketExists)
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrAccessDenied` | S3 `HeadBucket` or `DeleteBucket` answered 403"
func TestAccessDenied(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		s3Error(w, http.StatusForbidden, "AccessDenied")
	})
	p := f.provider(t, "us-east-1", "")
	_, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "forbidden"})
	wantErr(t, "HeadBucket(403)", err, storage.ErrAccessDenied)
	err = p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: "forbidden"})
	wantErr(t, "DeleteBucket(403)", err, storage.ErrAccessDenied)
}
