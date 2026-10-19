package internal

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

// TestIsValidBucketName checks valid and invalid bucket names.
func TestIsValidBucketName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bucket string
		want   bool
	}{
		{name: "minimum length", bucket: "abc", want: true},
		{name: "maximum length", bucket: strings.Repeat("a", 63), want: true},
		{name: "digits only", bucket: "123", want: true},
		{name: "dots and dashes", bucket: "my.bucket-name.v2", want: true},
		{name: "ip-like with letters", bucket: "192.168.1.1a", want: true},
		{name: "three octets only", bucket: "192.168.1", want: true},
		{name: "dot followed by dash", bucket: "a.-b", want: true},
		{name: "embedded reserved prefix", bucket: "my-xn--bucket", want: true},
		{name: "reserved suffix not at end", bucket: "a-s3alias-b", want: true},

		{name: "empty", bucket: "", want: false},
		{name: "too short", bucket: "ab", want: false},
		{name: "too long", bucket: strings.Repeat("a", 64), want: false},
		{name: "uppercase", bucket: "MyBucket", want: false},
		{name: "uppercase in middle", bucket: "myBucket", want: false},
		{name: "underscore", bucket: "my_bucket", want: false},
		{name: "space", bucket: "my bucket", want: false},
		{name: "slash", bucket: "my/bucket", want: false},
		{name: "backslash", bucket: `my\bucket`, want: false},
		{name: "non-ascii", bucket: "bücket", want: false},
		{name: "trailing newline", bucket: "bucket\n", want: false},
		{name: "nul byte", bucket: "buc\x00ket", want: false},
		{name: "leading dash", bucket: "-bucket", want: false},
		{name: "trailing dash", bucket: "bucket-", want: false},
		{name: "leading dot", bucket: ".bucket", want: false},
		{name: "trailing dot", bucket: "bucket.", want: false},
		{name: "double dot", bucket: "my..bucket", want: false},
		{name: "ip address", bucket: "192.168.1.1", want: false},
		{name: "ip-like out of range", bucket: "999.999.999.999", want: false},
		{name: "prefix xn--", bucket: "xn--bucket", want: false},
		{name: "prefix sthree-", bucket: "sthree-bucket", want: false},
		{name: "prefix sthree-configurator", bucket: "sthree-configurator", want: false},
		{name: "prefix amzn-s3-demo-", bucket: "amzn-s3-demo-bucket", want: false},
		{name: "suffix -s3alias", bucket: "bucket-s3alias", want: false},
		{name: "suffix --ol-s3", bucket: "bucket--ol-s3", want: false},
		{name: "suffix .mrap", bucket: "bucket.mrap", want: false},
		{name: "suffix --x-s3", bucket: "bucket--x-s3", want: false},
		{name: "suffix --table-s3", bucket: "bucket--table-s3", want: false},
		{name: "suffix -an", bucket: "bucket-an", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsValidBucketName(tt.bucket); got != tt.want {
				t.Errorf("IsValidBucketName(%q) = %v, want %v", tt.bucket, got, tt.want)
			}
		})
	}
}

// TestIsValidObjectKey checks valid and invalid object keys.
func TestIsValidObjectKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "single char", key: "a", want: true},
		{name: "nested", key: "a/b/c", want: true},
		{name: "trailing slash", key: "a/b/", want: true},
		{name: "spaces", key: "my file name.txt", want: true},
		{name: "unicode", key: "dossier/résumé-日本語.pdf", want: true},
		{name: "exactly 1024 bytes", key: strings.Repeat("a", 1024), want: true},
		{name: "1024 bytes of multibyte runes", key: strings.Repeat("é", 512), want: true},
		{name: "dotdot only", key: "..", want: true},
		{name: "parent escape", key: "../escape", want: true},
		{name: "absolute", key: "/abs", want: true},
		{name: "root slash", key: "/", want: true},
		{name: "resolves to sibling", key: "x/../y", want: true},
		{name: "dotfile", key: ".hidden", want: true},
		{name: "triple dot", key: "...", want: true},
		// Only "/" separates path elements, on every GOOS, as in S3: a
		// backslash is an ordinary byte, so none of these clean to ".".
		{name: "backslash dot", key: `a\.`, want: true},
		{name: "dot backslash", key: `.\`, want: true},
		{name: "backslash", key: `\`, want: true},
		{name: "backslash dotdot", key: `a\..`, want: true},
		{name: "backslash then dot segment", key: `a\b\.`, want: true},
		{name: "dot backslash dot", key: `.\.`, want: true},
		{name: "slash then backslash dot", key: `a/b\.`, want: true},

		{name: "empty", key: "", want: false},
		{name: "1025 bytes", key: strings.Repeat("a", 1025), want: false},
		{name: "513 two-byte runes is 1026 bytes", key: strings.Repeat("é", 513), want: false},
		{name: "invalid utf-8", key: "a\xffb", want: false},
		{name: "truncated utf-8", key: "a\xc3", want: false},
		{name: "dot", key: ".", want: false},
		{name: "dot slash", key: "./", want: false},
		{name: "trailing dot segment", key: "a/.", want: false},
		{name: "cleans to dot", key: "a/..", want: false},
		{name: "cleans to dot with trailing slash", key: "x/../", want: false},
		{name: "nested cleans to dot", key: "a/b/../..", want: false},
		{name: "dot after backslash segment", key: `a\/.`, want: false},
		{name: "backslash segment cleans to dot", key: `a\/..`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsValidObjectKey(tt.key); got != tt.want {
				t.Errorf("IsValidObjectKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestValidateInput checks key validation and bucket resolution.
func TestValidateInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input      storage.BaseObjectInput
		wantErr    error
		name       string
		cfgBucket  string
		wantBucket string
	}{
		{
			name:       "explicit bucket",
			input:      storage.BaseObjectInput{Bucket: new("explicit"), Key: "k"},
			cfgBucket:  "configured",
			wantBucket: "explicit",
		},
		{
			name:       "nil bucket falls back to config",
			input:      storage.BaseObjectInput{Key: "k"},
			cfgBucket:  "configured",
			wantBucket: "configured",
		},
		{
			name:      "pointer to empty bucket does not fall back",
			input:     storage.BaseObjectInput{Bucket: new(""), Key: "k"},
			cfgBucket: "configured",
			wantErr:   storage.ErrInvalidBucket,
		},
		{
			name:      "invalid explicit bucket does not fall back",
			input:     storage.BaseObjectInput{Bucket: new("Bad_Bucket"), Key: "k"},
			cfgBucket: "configured",
			wantErr:   storage.ErrInvalidBucket,
		},
		{
			name:    "no bucket anywhere",
			input:   storage.BaseObjectInput{Key: "k"},
			wantErr: storage.ErrInvalidBucket,
		},
		{
			name:      "invalid configured bucket",
			input:     storage.BaseObjectInput{Key: "k"},
			cfgBucket: "UPPER",
			wantErr:   storage.ErrInvalidBucket,
		},
		{
			name:      "zero-value key",
			input:     storage.BaseObjectInput{Bucket: new("bucket")},
			cfgBucket: "configured",
			wantErr:   storage.ErrInvalidKey,
		},
		{
			name:    "empty key",
			input:   storage.BaseObjectInput{Bucket: new("bucket"), Key: ""},
			wantErr: storage.ErrInvalidKey,
		},
		{
			name:    "dot key",
			input:   storage.BaseObjectInput{Bucket: new("bucket"), Key: "."},
			wantErr: storage.ErrInvalidKey,
		},
		{
			name:    "invalid key takes precedence over invalid bucket",
			input:   storage.BaseObjectInput{Bucket: new("B"), Key: ""},
			wantErr: storage.ErrInvalidKey,
		},
		{
			name:    "zero-value key and no bucket reports key",
			input:   storage.BaseObjectInput{},
			wantErr: storage.ErrInvalidKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := tt.input
			got, err := ValidateInput(&input, tt.cfgBucket)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ValidateInput() error = %v, want %v", err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("ValidateInput() bucket = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateInput() unexpected error: %v", err)
			}
			if got != tt.wantBucket {
				t.Errorf("ValidateInput() bucket = %q, want %q", got, tt.wantBucket)
			}
		})
	}
}

// testConfig is a configuration shape for BindConfig tests.
type testConfig struct {
	Storage struct {
		Bucket string
		Region string
	}
}

// TestBindConfig checks binding, a nil config provider, and a bind
// failure.
func TestBindConfig(t *testing.T) {
	t.Parallel()

	t.Run("binds values from config provider", func(t *testing.T) {
		t.Parallel()
		mock, err := configtest.NewMockConfigWithValues(map[string]any{
			"storage": map[string]any{"bucket": "my-bucket", "region": "eu-west-1"},
		})
		if err != nil {
			t.Fatalf("NewMockConfigWithValues() error: %v", err)
		}
		cfg, err := BindConfig[testConfig](mock)
		if err != nil {
			t.Fatalf("BindConfig() error: %v", err)
		}
		if cfg.Storage.Bucket != "my-bucket" || cfg.Storage.Region != "eu-west-1" {
			t.Errorf("BindConfig() = %+v, want bucket my-bucket region eu-west-1", cfg.Storage)
		}
		if mock.CallCount("Bind") != 1 {
			t.Errorf("Bind called %d times, want 1", mock.CallCount("Bind"))
		}
	})

	t.Run("empty config yields zero value", func(t *testing.T) {
		t.Parallel()
		cfg, err := BindConfig[testConfig](&configtest.MockConfig{})
		if err != nil {
			t.Fatalf("BindConfig() error: %v", err)
		}
		if cfg == nil {
			t.Fatal("BindConfig() returned nil config")
		}
		if cfg.Storage.Bucket != "" {
			t.Errorf("Bucket = %q, want empty", cfg.Storage.Bucket)
		}
	})

	t.Run("nil config provider", func(t *testing.T) {
		t.Parallel()
		cfg, err := BindConfig[testConfig](nil)
		if err == nil {
			t.Fatal("BindConfig() error = nil, want error")
		}
		if cfg != nil {
			t.Errorf("BindConfig() cfg = %+v, want nil", cfg)
		}
		if err.Error() != "config provider is nil" {
			t.Errorf("BindConfig() error = %q, want config provider is nil", err)
		}
	})

	t.Run("bind failure is wrapped", func(t *testing.T) {
		t.Parallel()
		bindErr := errors.New("boom")
		mock := &configtest.MockConfig{}
		mock.BindFn = func(any, ...config.BindOption) error { return bindErr }
		cfg, err := BindConfig[testConfig](mock)
		if !errors.Is(err, bindErr) {
			t.Fatalf("BindConfig() error = %v, want wrapping %v", err, bindErr)
		}
		if !strings.Contains(err.Error(), "bind config") {
			t.Errorf("BindConfig() error = %q, want it to mention bind config", err)
		}
		if cfg != nil {
			t.Errorf("BindConfig() cfg = %+v, want nil", cfg)
		}
	})
}

// TestErrorf checks the prefix, formatting, and wrapping of Errorf.
func TestErrorf(t *testing.T) {
	t.Parallel()

	t.Run("prefixes module and formats args", func(t *testing.T) {
		t.Parallel()
		err := Errorf("dir/provider", "op %s %d", "x", 42)
		want := "gas/storage/dir/provider: op x 42"
		if err.Error() != want {
			t.Errorf("Errorf() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("wraps with %w", func(t *testing.T) {
		t.Parallel()
		err := Errorf("s3/provider", "head: %w", storage.ErrKeyNotFound)
		if !errors.Is(err, storage.ErrKeyNotFound) {
			t.Errorf("errors.Is(%v, ErrKeyNotFound) = false", err)
		}
		want := "gas/storage/s3/provider: head: storage: key not found"
		if err.Error() != want {
			t.Errorf("Errorf() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("wraps multiple errors", func(t *testing.T) {
		t.Parallel()
		e1, e2 := errors.New("one"), errors.New("two")
		err := Errorf("m", "%w and %w", e1, e2)
		if !errors.Is(err, e1) || !errors.Is(err, e2) {
			t.Errorf("Errorf() = %v, want to wrap both errors", err)
		}
	})

	t.Run("percent in args is not reinterpreted", func(t *testing.T) {
		t.Parallel()
		err := Errorf("m", "key %q", "100%d")
		want := `gas/storage/m: key "100%d"`
		if err.Error() != want {
			t.Errorf("Errorf() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("no args", func(t *testing.T) {
		t.Parallel()
		err := Errorf("m", "plain")
		if err.Error() != "gas/storage/m: plain" {
			t.Errorf("Errorf() = %q", err.Error())
		}
		if errors.Unwrap(err) != nil {
			t.Errorf("errors.Unwrap() = %v, want nil", errors.Unwrap(err))
		}
	})
}

// badCountReader is a reader whose Read breaks the io.Reader contract: it
// returns len(p)+over when over is set, and the negative count neg
// otherwise, with no error.
type badCountReader struct{ over, neg int }

func (b badCountReader) Read(p []byte) (int, error) {
	if b.over != 0 {
		return len(p) + b.over, nil
	}
	return b.neg, nil
}

// TestCheckLengthInvalidCount checks that a reader that breaks the io.Reader
// contract with a count below zero or above len(p) gets a sticky error, not a
// panic or a miscount, while reading and while probing. It is not a length
// mismatch.
func TestCheckLengthInvalidCount(t *testing.T) {
	t.Parallel()
	for name, bad := range map[string]badCountReader{
		"len(p)+5": {over: 5},
		"-3":       {neg: -3},
	} {
		t.Run("reader returning "+name, func(t *testing.T) {
			t.Parallel()
			n := int64(100)
			r, err := CheckLength(bad, &n)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				k, err := r.Read(make([]byte, 8))
				if k != 0 || err == nil || errors.Is(err, storage.ErrContentLengthMismatch) ||
					!strings.Contains(err.Error(), "invalid count") {
					t.Fatalf("Read() = %d, %v; want 0 and a sticky invalid count error", k, err)
				}
			}
		})
	}
	t.Run("reader returning an invalid count while probing", func(t *testing.T) {
		t.Parallel()
		n := int64(2)
		r, err := CheckLength(io.MultiReader(strings.NewReader("ab"), badCountReader{over: 5}), &n)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		if err == nil || errors.Is(err, storage.ErrContentLengthMismatch) ||
			!strings.Contains(err.Error(), "invalid count") {
			t.Fatalf("ReadAll() error = %v, want an invalid count error", err)
		}
	})
}

// zeroReads is a reader that returns 0, nil a few times before each byte,
// which io.Reader allows but discourages.
type zeroReads struct {
	data  []byte
	calls int
}

func (z *zeroReads) Read(p []byte) (int, error) {
	z.calls++
	if z.calls%3 != 0 {
		return 0, nil
	}
	if len(z.data) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0], z.data = z.data[0], z.data[1:]
	return 1, nil
}

// TestCheckLength checks the reader CheckLength returns: it yields the body
// unchanged when the length matches, and fails with ErrContentLengthMismatch
// as soon as the body is shorter or longer than declared.
func TestCheckLength(t *testing.T) {
	t.Parallel()

	read := func(t *testing.T, body io.Reader, n *int64) ([]byte, error) {
		t.Helper()
		r, err := CheckLength(body, n)
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}

	t.Run("nil length passes the body through", func(t *testing.T) {
		t.Parallel()
		body := strings.NewReader("abc")
		r, err := CheckLength(body, nil)
		if err != nil || r != io.Reader(body) {
			t.Fatalf("CheckLength(body, nil) = %v, %v; want the body itself", r, err)
		}
	})

	for _, tc := range []struct {
		body     io.Reader
		name     string
		want     string
		length   int64
		mismatch bool
	}{
		{name: "exact", body: strings.NewReader("abcde"), length: 5, want: "abcde"},
		{name: "exact empty", body: strings.NewReader(""), length: 0, want: ""},
		{name: "nil body with zero length", body: nil, length: 0, want: ""},
		{name: "nil body with a length", body: nil, length: 3, mismatch: true},
		{name: "short", body: strings.NewReader("abc"), length: 5, mismatch: true},
		{name: "long", body: strings.NewReader("abcdefg"), length: 5, mismatch: true},
		{name: "long by one", body: strings.NewReader("abcdef"), length: 5, mismatch: true},
		{name: "zero length with data", body: strings.NewReader("a"), length: 0, mismatch: true},
		{name: "negative", body: strings.NewReader("abc"), length: -1, mismatch: true},
		{name: "one byte reads, exact", body: iotest.OneByteReader(strings.NewReader("abcde")), length: 5, want: "abcde"},
		{name: "one byte reads, long", body: iotest.OneByteReader(strings.NewReader("abcdef")), length: 5, mismatch: true},
		{name: "data with EOF, exact", body: iotest.DataErrReader(strings.NewReader("abcde")), length: 5, want: "abcde"},
		{name: "data with EOF, short", body: iotest.DataErrReader(strings.NewReader("abc")), length: 5, mismatch: true},
		{name: "zero reads, exact", body: &zeroReads{data: []byte("abc")}, length: 3, want: "abc"},
		{name: "zero reads, long", body: &zeroReads{data: []byte("abcd")}, length: 3, mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := read(t, tc.body, &tc.length)
			if tc.mismatch {
				if !errors.Is(err, storage.ErrContentLengthMismatch) {
					t.Fatalf("read = %q, %v; want ErrContentLengthMismatch", got, err)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("read = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestCheckLengthReaderBehavior checks how the reader CheckLength returns
// handles a caller that stops at the declared length, a body that fails or
// breaks the io.Reader contract, and repeated reads after an error.
func TestCheckLengthReaderBehavior(t *testing.T) {
	t.Parallel()

	read := func(t *testing.T, body io.Reader, n *int64) ([]byte, error) {
		t.Helper()
		r, err := CheckLength(body, n)
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}

	t.Run("a long body is caught even when only the declared bytes are read", func(t *testing.T) {
		t.Parallel()
		n := int64(5)
		r, err := CheckLength(strings.NewReader("abcdefg"), &n)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 5)
		if _, err := io.ReadFull(r, buf); !errors.Is(err, storage.ErrContentLengthMismatch) {
			t.Fatalf(
				"ReadFull of the declared length error = %v, want ErrContentLengthMismatch",
				err,
			)
		}
	})

	t.Run("a read error passes through", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("boom")
		n := int64(5)
		_, err := read(t, io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(boom)), &n)
		if !errors.Is(err, boom) || errors.Is(err, storage.ErrContentLengthMismatch) {
			t.Fatalf("error = %v, want boom only", err)
		}
	})

	t.Run("the error is sticky", func(t *testing.T) {
		t.Parallel()
		n := int64(5)
		r, err := CheckLength(strings.NewReader("abc"), &n)
		if err != nil {
			t.Fatal(err)
		}
		_, first := io.ReadAll(r)
		k, again := r.Read(make([]byte, 8))
		if !errors.Is(first, storage.ErrContentLengthMismatch) || k != 0 ||
			!errors.Is(again, storage.ErrContentLengthMismatch) {
			t.Fatalf("first = %v, then %d, %v; want the mismatch both times", first, k, again)
		}
	})
}

// TestValidateListObjects checks bucket resolution, the MaxKeys range and
// cap, and the empty continuation token, in that order.
func TestValidateListObjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want       error
		in         storage.ListObjectsInput
		name       string
		cfgBucket  string
		wantBucket string
		wantMax    int
	}{
		{name: "defaults", cfgBucket: "cfg-bucket", wantBucket: "cfg-bucket", wantMax: 1000},
		{
			name:       "explicit bucket",
			in:         storage.ListObjectsInput{Bucket: new("my-bucket")},
			cfgBucket:  "cfg-bucket",
			wantBucket: "my-bucket",
			wantMax:    1000,
		},
		{name: "no bucket", want: storage.ErrInvalidBucket},
		{
			name:      "empty explicit bucket",
			in:        storage.ListObjectsInput{Bucket: new("")},
			cfgBucket: "cfg-bucket",
			want:      storage.ErrInvalidBucket,
		},
		{
			name:       "max keys one",
			in:         storage.ListObjectsInput{MaxKeys: new(int32(1))},
			cfgBucket:  "cfg-bucket",
			wantBucket: "cfg-bucket",
			wantMax:    1,
		},
		{
			name:       "max keys capped",
			in:         storage.ListObjectsInput{MaxKeys: new(int32(5000))},
			cfgBucket:  "cfg-bucket",
			wantBucket: "cfg-bucket",
			wantMax:    1000,
		},
		{
			name:      "max keys zero",
			in:        storage.ListObjectsInput{MaxKeys: new(int32(0))},
			cfgBucket: "cfg-bucket",
			want:      storage.ErrInvalidLimit,
		},
		{
			name:      "max keys negative",
			in:        storage.ListObjectsInput{MaxKeys: new(int32(-1))},
			cfgBucket: "cfg-bucket",
			want:      storage.ErrInvalidLimit,
		},
		{
			name: "bucket checked before max keys",
			in:   storage.ListObjectsInput{MaxKeys: new(int32(0))},
			want: storage.ErrInvalidBucket,
		},
		{
			name:      "empty token",
			in:        storage.ListObjectsInput{ContinuationToken: new("")},
			cfgBucket: "cfg-bucket",
			want:      storage.ErrInvalidContinuationToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bucket, maxKeys, err := ValidateListObjects(&tt.in, tt.cfgBucket)
			if !errors.Is(err, tt.want) || bucket != tt.wantBucket || maxKeys != tt.wantMax {
				t.Errorf("ValidateListObjects() = %q, %d, %v; want %q, %d, %v",
					bucket, maxKeys, err, tt.wantBucket, tt.wantMax, tt.want)
			}
		})
	}
}

// TestValidateListBuckets checks the MaxBuckets range and the empty
// continuation token.
func TestValidateListBuckets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want    error
		in      storage.ListBucketsInput
		name    string
		wantMax int
	}{
		{name: "defaults", wantMax: 10000},
		{name: "one", in: storage.ListBucketsInput{MaxBuckets: new(int32(1))}, wantMax: 1},
		{
			name:    "ten thousand",
			in:      storage.ListBucketsInput{MaxBuckets: new(int32(10000))},
			wantMax: 10000,
		},
		{
			name: "zero",
			in:   storage.ListBucketsInput{MaxBuckets: new(int32(0))},
			want: storage.ErrInvalidLimit,
		},
		{
			name: "over ten thousand",
			in:   storage.ListBucketsInput{MaxBuckets: new(int32(10001))},
			want: storage.ErrInvalidLimit,
		},
		{
			name: "empty token",
			in:   storage.ListBucketsInput{ContinuationToken: new("")},
			want: storage.ErrInvalidContinuationToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			maxBuckets, err := ValidateListBuckets(&tt.in)
			if !errors.Is(err, tt.want) || maxBuckets != tt.wantMax {
				t.Errorf("ValidateListBuckets() = %d, %v; want %d, %v",
					maxBuckets, err, tt.wantMax, tt.want)
			}
		})
	}
}
