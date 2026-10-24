package modulesstorage_test

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// Docs: modules/storage.mdx#configuration
// "The constructors bind these keys from the `gas.ConfigProvider`, under
// `storage`. Keys are case-insensitive"
func TestConfigKeysCaseInsensitive(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	cfg := mockConfig(t, map[string]any{
		"BUCKET": "upper-bucket", "Region": "eu-west-3", "ACCESSKEYID": "AKIDUPPER",
		"SecretAccessKey": "s", "ENDPOINT": f.URL,
	})
	pre, err := s3svc.NewPresigner()(cfg)
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	up, _ := presignBoth(t, pre, nil)
	u, _ := url.Parse(up.URL)
	cred := query(t, up.URL).Get("X-Amz-Credential")
	if u.Path != "/upper-bucket/k" || !strings.HasPrefix(cred, "AKIDUPPER/") || !strings.Contains(cred, "/eu-west-3/s3/") ||
		!strings.HasPrefix(up.URL, f.URL) {
		t.Fatalf("URL %s, credential %s, want the bucket, key ID, region and endpoint bound from mixed-case keys", up.URL, cred)
	}
	root, _ := openRoot(t)
	d, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{"BuCkEt": "dir-bucket"}))
	if err != nil {
		t.Fatalf("dir.NewProvider() error: %v", err)
	}
	mustCreateBucket(t, d, "dir-bucket")
	mustPut(t, d, "k", "v")
	if got := readObject(t, d, "k"); got != "v" {
		t.Fatalf("dir default bucket from key BuCkEt not used")
	}
}

// Docs: modules/storage.mdx#configuration
// "`storage.bucket` | string | | Bucket for inputs with a nil `Bucket` (s3 and
// dir). Optional; must be a valid S3 bucket name when set"
func TestBucketConfig(t *testing.T) {
	t.Parallel()
	_, rootPath := openRoot(t)
	cases := map[string]func(v map[string]any) error{
		"s3 provider": func(v map[string]any) error {
			_, err := s3svc.NewProvider()(mockConfig(t, v))
			return err
		},
		"s3 presigner": func(v map[string]any) error {
			_, err := s3svc.NewPresigner()(mockConfig(t, v))
			return err
		},
		"dir provider": func(v map[string]any) error {
			r, err := os.OpenRoot(rootPath)
			if err != nil {
				return err
			}
			defer func() { _ = r.Close() }()
			_, err = storagedir.NewProvider(storagedir.WithRoot(r))(mockConfig(t, v))
			return err
		},
	}
	for name, build := range cases {
		base := map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s"}
		if err := build(base); err != nil {
			t.Errorf("%s without storage.bucket error = %v, want nil (optional)", name, err)
		}
		for _, bad := range []string{"Not_A_Valid_Bucket", "ab", "-lead", "a..b", "192.168.1.1"} {
			v := map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s", "bucket": bad}
			wantErr(t, name+" storage.bucket "+bad, build(v), storage.ErrInvalidBucket)
		}
	}
}

// Docs: modules/storage.mdx#configuration
// "`storage.region` | string | | AWS region (s3). Empty uses the AWS SDK's
// default resolution from the environment and shared config"
func TestRegionConfig(t *testing.T) {
	isolateAWSEnv(t)
	f := newFakeS3(t, nil)
	scope := func(region string) string {
		up, _ := presignBoth(t, f.presigner(t, region, "bkt"), nil)
		return query(t, up.URL).Get("X-Amz-Credential")
	}
	if got := scope("eu-west-3"); !strings.Contains(got, "/eu-west-3/s3/") {
		t.Fatalf("configured region: X-Amz-Credential = %q, want scope eu-west-3", got)
	}
	t.Setenv("AWS_REGION", "ap-south-1")
	if got := scope(""); !strings.Contains(got, "/ap-south-1/s3/") {
		t.Fatalf("empty region with AWS_REGION: X-Amz-Credential = %q, want scope ap-south-1 from the environment", got)
	}
	t.Setenv("AWS_REGION", "")
	shared := t.TempDir() + "/config"
	if err := os.WriteFile(shared, []byte("[default]\nregion = sa-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", shared)
	if got := scope(""); !strings.Contains(got, "/sa-east-1/s3/") {
		t.Fatalf("empty region with shared config: X-Amz-Credential = %q, want scope sa-east-1 from shared config", got)
	}
}

// Docs: modules/storage.mdx#configuration
// "`storage.accesskeyid` | string | | Static access key ID (s3). When empty, the
// SDK's default credential chain applies" and "`storage.secretaccesskey` | string
// | | Secret for `accesskeyid` (s3)" and "`storage.sessiontoken` | string | |
// Optional session token for `accesskeyid` (s3)"
func TestCredentialConfig(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")

	static, err := s3svc.NewPresigner()(mockConfig(t, map[string]any{
		"region": "us-east-1", "bucket": "bkt", "accessKeyID": "AKIDSTATIC",
		"secretAccessKey": "s", "sessionToken": "TOKEN123",
	}))
	if err != nil {
		t.Fatalf("NewPresigner(static) error: %v", err)
	}
	up, _ := presignBoth(t, static, nil)
	q := query(t, up.URL)
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIDSTATIC/") || q.Get("X-Amz-Security-Token") != "TOKEN123" {
		t.Fatalf("static: credential %q, token %q, want AKIDSTATIC and TOKEN123", q.Get("X-Amz-Credential"), q.Get("X-Amz-Security-Token"))
	}
	noToken, err := s3svc.NewPresigner()(mockConfig(t, map[string]any{
		"region": "us-east-1", "bucket": "bkt", "accessKeyID": "AKIDSTATIC", "secretAccessKey": "s",
	}))
	if err != nil {
		t.Fatalf("NewPresigner(static, no token) error: %v", err)
	}
	up, _ = presignBoth(t, noToken, nil)
	if q := query(t, up.URL); q.Has("X-Amz-Security-Token") {
		t.Fatalf("static without session token: URL carries X-Amz-Security-Token, want it optional")
	}

	chain, err := s3svc.NewPresigner()(mockConfig(t, map[string]any{"region": "us-east-1", "bucket": "bkt"}))
	if err != nil {
		t.Fatalf("NewPresigner(default chain) error: %v", err)
	}
	up, _ = presignBoth(t, chain, nil)
	if got := query(t, up.URL).Get("X-Amz-Credential"); !strings.HasPrefix(got, "AKIDFROMENV/") {
		t.Fatalf("default chain: X-Amz-Credential = %q, want the environment's AKIDFROMENV", got)
	}
}

// Docs: modules/storage.mdx#configuration
// "`storage.endpoint` | string | | Custom endpoint for S3-compatible services
// (s3). Setting it also switches to path-style addressing"
func TestEndpointPathStyle(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	up, down := presignBoth(t, f.presigner(t, "us-east-1", "mybucket"), nil)
	ep, _ := url.Parse(f.URL)
	for kind, r := range map[string]*storage.PresignURLResponse{"upload": up, "download": down} {
		u, _ := url.Parse(r.URL)
		if u.Host != ep.Host || u.Path != "/mybucket/k" {
			t.Fatalf("%s URL = %s, want host %s and path-style /mybucket/k", kind, r.URL, ep.Host)
		}
	}
	p := f.provider(t, "us-east-1", "mybucket")
	_, _ = p.HeadObject(t.Context(), headIn("k"))
	if paths := f.paths(); len(paths) == 0 || !strings.HasPrefix(paths[0], "/mybucket/k") {
		t.Fatalf("provider requests %v, want path-style /mybucket/k on the custom endpoint", paths)
	}
}

// Docs: modules/storage.mdx#configuration
// "The directory backend reads only `storage.bucket`."
// Checked as: s3 keys that would make an s3 constructor fail or misbehave do not
// affect the dir backend.
func TestDirReadsOnlyBucket(t *testing.T) {
	t.Parallel()
	root, _ := openRoot(t)
	p, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{
		"bucket": "only", "region": "not a region", "endpoint": "::not a url", "accessKeyID": "x",
	}))
	if err != nil {
		t.Fatalf("dir.NewProvider() with s3 keys set error = %v, want them ignored", err)
	}
	mustCreateBucket(t, p, "only")
	mustPut(t, p, "k", "v")
	if got := readObject(t, p, "k"); got != "v" {
		t.Fatalf("read %q, want v", got)
	}
}
