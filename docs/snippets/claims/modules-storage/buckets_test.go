package modulesstorage_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// Docs: modules/storage.mdx#operations
// "`CreateBucket(ctx, *CreateBucketInput)` | `*Bucket` | A bucket you already
// own is success"
// Docs: modules/storage.mdx#buckets
// "`CreateBucket` of a bucket you already own succeeds on both backends."
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
			if readObject(t, b.p, "kept") != "x" {
				t.Fatalf("object lost after CreateBucket of an owned bucket")
			}
		})
	}
}

// Docs: modules/storage.mdx#buckets
// "For a bucket you already own it succeeds, and the returned `Bucket` may hold
// only the name."
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

// Docs: modules/storage.mdx#buckets
// "`storage.ErrBucketExists` means another account owns the name, which only the
// s3 backend can report."
func TestBucketExists(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusConflict, "BucketAlreadyExists")
	})
	_, err := f.provider(t, "us-east-1", "").CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "taken"})
	wantErr(t, "s3 CreateBucket(taken by another account)", err, storage.ErrBucketExists)
}

// Docs: modules/storage.mdx#operations
// "`DeleteBucket(ctx, *DeleteBucketInput)` | `error` | Deletes an empty bucket; a
// missing one is `ErrBucketNotFound`"
// Docs: modules/storage.mdx#buckets
// "`DeleteBucket` deletes an empty bucket: one that holds objects is
// `storage.ErrBucketNotEmpty`, and a missing one `storage.ErrBucketNotFound`.
// Unlike `DeleteObject` of a missing key, which succeeds, deleting a missing
// bucket is an error, as on S3. `HeadBucket` of a missing bucket is
// `storage.ErrBucketNotFound`."
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

// Docs: modules/storage.mdx#operations
// "`HeadBucket(ctx, *HeadBucketInput)` | `*Bucket` | Whether a bucket exists, and
// what the backend knows about it"
// Docs: modules/storage.mdx#buckets
// "`HeadBucket` | `Name`, `ID` (ARN), `Region` | `Name`, and `CreationDate`
// unless the bucket's name record is damaged"
func TestHeadBucketFields(t *testing.T) {
	t.Parallel()
	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueName("hb")
		p := newS3Provider(t, "eu-west-1", "")
		mustCreateBucket(t, p, bucket)
		got, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("HeadBucket() error: %v", err)
		}
		if got.Name != bucket || !strings.HasPrefix(got.ID, "arn:") || !strings.HasSuffix(got.ID, bucket) || got.Region != "eu-west-1" {
			t.Fatalf("s3 HeadBucket() = %+v, want Name, ID as the ARN, Region eu-west-1", got)
		}
	})
	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		p, root := newDirProvider(t, "")
		bucket := uniqueName("hb")
		mustCreateBucket(t, p, bucket)
		got, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("HeadBucket() error: %v", err)
		}
		if got.Name != bucket || got.CreationDate.IsZero() {
			t.Fatalf("dir HeadBucket() = %+v, want Name and CreationDate", got)
		}
		if err := os.Remove(filepath.Join(root, ".gas", "buckets", sha(bucket))); err != nil {
			t.Fatalf("remove name record: %v", err)
		}
		got, err = p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("HeadBucket(damaged name record) error: %v", err)
		}
		if got.Name != bucket || !got.CreationDate.IsZero() {
			t.Fatalf("dir HeadBucket(damaged name record) = %+v, want Name and no CreationDate", got)
		}
	})
}

// Docs: modules/storage.mdx#buckets
// "`CreateBucket` | `Name`, and the ARN as `ID` | `Name`, `CreationDate`"
// Assumption: a field the table does not list for an operation is empty.
func TestCreateBucketFields(t *testing.T) {
	t.Parallel()
	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueName("cb")
		p := newS3Provider(t, "us-east-1", "")
		got, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("CreateBucket() error: %v", err)
		}
		if got.Name != bucket || !strings.HasPrefix(got.ID, "arn:") || !strings.HasSuffix(got.ID, bucket) {
			t.Fatalf("s3 CreateBucket() = %+v, want Name and the ARN as ID", got)
		}
		if !got.CreationDate.IsZero() || got.Region != "" {
			t.Fatalf("s3 CreateBucket() = %+v, want no CreationDate or Region", got)
		}
	})
	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		p, _ := newDirProvider(t, "")
		bucket := uniqueName("cb")
		got, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("CreateBucket() error: %v", err)
		}
		if got.Name != bucket || got.CreationDate.IsZero() || got.ID != "" || got.Region != "" {
			t.Fatalf("dir CreateBucket() = %+v, want Name and CreationDate only", got)
		}
	})
}

// Docs: modules/storage.mdx#buckets
// "`ListBuckets` | `Name`, `CreationDate`, and the ARN and region when S3 sends
// them | `Name`, `CreationDate`"
func TestListBucketsFields(t *testing.T) {
	t.Parallel()
	dp, _ := newDirProvider(t, "")
	for name, p := range map[string]storage.Provider{"s3": newS3Provider(t, "us-east-1", ""), "dir": dp} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bucket := uniqueName("lbf")
			mustCreateBucket(t, p, bucket)
			out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{Prefix: ptr(bucket)})
			if err != nil {
				t.Fatalf("ListBuckets() error: %v", err)
			}
			if len(out.Buckets) != 1 || out.Buckets[0].Name != bucket || out.Buckets[0].CreationDate.IsZero() {
				t.Fatalf("ListBuckets() = %+v, want %s with Name and CreationDate", out.Buckets, bucket)
			}
			if got := out.Buckets[0]; name == "dir" && (got.ID != "" || got.Region != "") {
				t.Fatalf("dir ListBuckets() = %+v, want Name and CreationDate only", got)
			}
		})
	}
}

// Docs: modules/storage.mdx#buckets
// "On the directory backend, a bucket's `CreationDate` is when `CreateBucket`
// first ran for it."
func TestDirCreationDateIsFirstCreate(t *testing.T) {
	t.Parallel()
	p, _ := newDirProvider(t, "")
	bucket := uniqueName("cd")
	before := time.Now()
	first, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	after := time.Now()
	if first.CreationDate.Before(before.Add(-time.Second)) || first.CreationDate.After(after.Add(time.Second)) {
		t.Fatalf("CreationDate = %v, want between %v and %v", first.CreationDate, before, after)
	}
	time.Sleep(1100 * time.Millisecond)
	again, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("CreateBucket(again) error: %v", err)
	}
	head, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("HeadBucket() error: %v", err)
	}
	if !again.CreationDate.Equal(first.CreationDate) || !head.CreationDate.Equal(first.CreationDate) {
		t.Fatalf("CreationDate first %v, after second CreateBucket %v, HeadBucket %v, want the first", first.CreationDate, again.CreationDate, head.CreationDate)
	}
}

// Docs: modules/storage.mdx#buckets
// "On S3, `CreateBucket` creates the bucket in the client's region, and outside
// us-east-1 and `aws-global` it sends the location constraint S3 requires there"
func TestCreateBucketLocationConstraint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		region     string
		constraint bool
	}{{"us-east-1", false}, {"aws-global", false}, {"eu-west-1", true}, {"ap-southeast-2", true}} {
		t.Run(tc.region, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			if _, err := f.provider(t, tc.region, "").CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "loc-bucket"}); err != nil {
				t.Fatalf("CreateBucket() error: %v", err)
			}
			f.mu.Lock()
			body := strings.Join(f.bodies, "")
			f.mu.Unlock()
			has := strings.Contains(body, "<LocationConstraint>"+tc.region+"</LocationConstraint>")
			if has != tc.constraint || (!tc.constraint && strings.Contains(body, "LocationConstraint")) {
				t.Fatalf("region %s: body %q, location constraint sent = %v, want %v", tc.region, body, has, tc.constraint)
			}
		})
	}
	t.Run("localstack region", func(t *testing.T) {
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
	})
}

// Docs: modules/storage.mdx#buckets
// "with no configured region it uses the region the AWS SDK resolved."
func TestCreateBucketSDKResolvedRegion(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_REGION", "eu-central-1")
	f := newFakeS3(t, nil)
	if _, err := f.provider(t, "", "").CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "sdk-region"}); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	f.mu.Lock()
	body := strings.Join(f.bodies, "")
	f.mu.Unlock()
	if !strings.Contains(body, "<LocationConstraint>eu-central-1</LocationConstraint>") {
		t.Fatalf("CreateBucket body = %q, want the SDK-resolved region eu-central-1 as location constraint", body)
	}
}

// Docs: modules/storage.mdx#buckets
// "`HeadBucket` and `DeleteBucket` report any `403` as
// `storage.ErrAccessDenied`"
func TestAccessDenied(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"AccessDenied", "AllAccessDisabled", "SomethingElse"} {
		f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			s3Error(w, http.StatusForbidden, code)
		})
		p := f.provider(t, "us-east-1", "")
		_, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "forbidden"})
		wantErr(t, "HeadBucket(403)", err, storage.ErrAccessDenied)
		err = p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: "forbidden"})
		wantErr(t, "DeleteBucket(403 "+code+")", err, storage.ErrAccessDenied)
	}
}
