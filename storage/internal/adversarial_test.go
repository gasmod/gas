package internal

import (
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestIsValidBucketName_Boundaries checks bucket names at the edges of each
// naming rule.
func TestIsValidBucketName_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bucket string
		want   bool
	}{
		{name: "five octets is not an ip", bucket: "1.2.3.4.5", want: true},
		{name: "ip with trailing label", bucket: "1.2.3.4-a", want: true},
		{name: "zero-padded ip", bucket: "01.02.03.04", want: false},
		{name: "ip with leading zeros and long octets", bucket: "0001.2.3.4", want: false},
		{name: "dash before dot", bucket: "a-.b", want: true},
		{name: "dash dot dash", bucket: "a-.-b", want: true},
		{name: "63 with dots", bucket: strings.Repeat("a.", 31) + "a", want: true},
		{name: "64 with dots", bucket: strings.Repeat("a.", 31) + "ab", want: false},
		{name: "suffix -an inside", bucket: "japan-bucket", want: true},
		{name: "ends in an without dash", bucket: "japan", want: true},
		{name: "ends in --an", bucket: "bucket--an", want: false},
		{name: "reserved prefix exact", bucket: "xn--", want: false},
		{name: "unicode digit", bucket: "abc٣", want: false},
		{name: "fullwidth letters", bucket: "ａｂｃ", want: false},
		// Documents a consequence of the accepted reserved-suffix rule: names
		// in an S3 account regional namespace (<prefix>-<account>-<region>-an)
		// are legitimate, account-owned buckets and are rejected.
		{
			name:   "account regional namespace bucket",
			bucket: "logs-123456789012-us-east-1-an",
			want:   false,
		},
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

// TestIsValidObjectKey_Boundaries checks keys at the length and encoding
// limits.
func TestIsValidObjectKey_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "multibyte rune crosses 1024", key: strings.Repeat("a", 1023) + "é", want: false},
		{name: "three-byte runes up to 1024", key: strings.Repeat("日", 341) + "a", want: true},
		{name: "four-byte rune at the end", key: strings.Repeat("a", 1020) + "😀", want: true},
		{
			name: "four-byte rune crossing the end",
			key:  strings.Repeat("a", 1021) + "😀",
			want: false,
		},
		{name: "nul byte", key: "a\x00b", want: true},
		{name: "dot segment in the middle", key: "a/./b", want: true},
		{name: "leading dot slash", key: "./a", want: true},
		{name: "only slashes", key: "//", want: true},
		{name: "dot dot slash", key: "../", want: true},
		{name: "surrogate half encoded", key: "\xed\xa0\x80", want: false},
		{name: "overlong encoding", key: "\xc0\xaf", want: false},
		{name: "byte order mark", key: "\ufeffkey", want: true},
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

// TestErrorf_PercentInModule checks that the module name is used literally.
// Errorf builds its format string with Sprintf, so a '%' in mod becomes part
// of the format. No current caller passes such a module name.
func TestErrorf_PercentInModule(t *testing.T) {
	t.Parallel()
	err := Errorf("mod%", "op: %w", storage.ErrKeyNotFound)
	want := "gas/storage/mod%: op: storage: key not found"
	if err.Error() != want || !errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("BUG (latent): Errorf(%q, %q, err) = %q (wraps=%v), want %q wrapping the "+
			"error: mod is interpolated into the format string, so '%%' in it consumes "+
			"arguments and breaks %%w", "mod%", "op: %w", err, errors.Is(err,
			storage.ErrKeyNotFound), want)
	}
}

// TestErrorf_FormatVerbsInArgs checks that format verbs inside arguments are
// not interpreted.
func TestErrorf_FormatVerbsInArgs(t *testing.T) {
	t.Parallel()
	err := Errorf("dir/provider", "%s %q: %w", "get", "key%w%v%!", storage.ErrInvalidKey)
	want := `gas/storage/dir/provider: get "key%w%v%!": storage: invalid key`
	if err.Error() != want || !errors.Is(err, storage.ErrInvalidKey) {
		t.Errorf("Errorf() = %q, want %q", err, want)
	}
}
