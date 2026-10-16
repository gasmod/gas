package internal

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

// Reserved bucket name prefixes and suffixes, and the IPv4-like shape, that
// S3's general purpose bucket naming rules forbid.
var (
	invalidBucketNamePrefixes = []string{
		"xn--",
		"sthree-",
		"amzn-s3-demo-",
	}
	invalidBucketNameSuffixes = []string{
		"-s3alias",
		"--ol-s3",
		".mrap",
		"--x-s3",
		"--table-s3",
		"-an",
	}
	ipLike = regexp.MustCompile(`^(\d+\.){3}\d+$`)
)

// IsValidBucketName reports whether name is a valid S3 general purpose bucket
// name: 3 to 63 bytes of lowercase letters, digits, dots, and hyphens,
// starting and ending with a letter or digit, with no "..", no reserved
// prefix or suffix, and not shaped like an IPv4 address.
func IsValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}

	if !isAlnum(name[0]) || !isAlnum(name[len(name)-1]) {
		return false
	}

	if strings.Contains(name, "..") {
		return false
	}

	if hasInvalidBytes(name) {
		return false
	}

	if hasInvalidPrefix(name) {
		return false
	}

	if hasInvalidSuffix(name) {
		return false
	}

	if ipLike.MatchString(name) {
		return false
	}

	return true
}

// hasInvalidBytes reports whether name holds a byte other than a lowercase
// letter, digit, dot, or hyphen.
func hasInvalidBytes(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; !isAlnum(c) && c != '.' && c != '-' {
			return true
		}
	}
	return false
}

// isAlnum reports whether c is a lowercase ASCII letter or a digit.
func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// hasInvalidPrefix reports whether name starts with a reserved prefix.
func hasInvalidPrefix(name string) bool {
	for _, prefix := range invalidBucketNamePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// hasInvalidSuffix reports whether name ends with a reserved suffix.
func hasInvalidSuffix(name string) bool {
	for _, suffix := range invalidBucketNameSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// IsValidObjectKey reports whether key is acceptable to every backend: 1 to
// 1024 bytes of valid UTF-8 whose last "/"-separated element is not "." and
// that does not clean to "." as a slash-separated path. It uses package path,
// not path/filepath, so "/" is the only separator on every GOOS, as in S3,
// and a backslash is an ordinary byte. Backends may reject more keys.
func IsValidObjectKey(key string) bool {
	if len(key) < 1 || len(key) > 1024 {
		return false
	}

	if !utf8.ValidString(key) {
		return false
	}

	if path.Base(key) == "." || path.Clean(key) == "." {
		return false
	}

	return true
}

// BindConfig binds a new T from cfgProvider. It returns an error when
// cfgProvider is nil or binding fails; the error is meant to be wrapped by the
// caller.
func BindConfig[T any](cfgProvider gas.ConfigProvider) (*T, error) {
	if cfgProvider == nil {
		return nil, errors.New("config provider is nil")
	}

	cfg := new(T)
	if cfgErr := cfgProvider.Bind(cfg); cfgErr != nil {
		return nil, fmt.Errorf("bind config: %w", cfgErr)
	}

	return cfg, nil
}

// ValidateInput checks input's key and resolves the bucket to use: the
// explicit Bucket when set, otherwise cfgBucket. It returns
// storage.ErrInvalidKey for a bad key (checked first) and
// storage.ErrInvalidBucket for a bad explicit bucket or, when Bucket is nil,
// a bad or empty cfgBucket. The errors are bare sentinels for the caller to
// wrap.
func ValidateInput(input *storage.BaseObjectInput, cfgBucket string) (string, error) {
	if !IsValidObjectKey(input.Key) {
		return "", storage.ErrInvalidKey
	}

	if input.Bucket != nil {
		if !IsValidBucketName(*input.Bucket) {
			return "", storage.ErrInvalidBucket
		}
		return *input.Bucket, nil
	}

	if IsValidBucketName(cfgBucket) {
		return cfgBucket, nil
	}

	return "", storage.ErrInvalidBucket
}

// Errorf formats an error like fmt.Errorf, prefixed with
// "gas/storage/<mod>: ". mod is used literally, so a '%' in it is not a
// verb. Use %w in format to wrap.
func Errorf(mod, format string, args ...any) error {
	prefix := "gas/storage/" + strings.ReplaceAll(mod, "%", "%%") + ": "
	return fmt.Errorf(prefix+format, args...)
}
