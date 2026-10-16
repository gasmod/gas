package internal

import (
	"errors"
	"strings"
	"testing"

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
