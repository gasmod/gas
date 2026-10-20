package s3

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestProvider_PutObjectCondition checks the conditional-write headers
// PutObject sends and how S3's 412 maps.
func TestProvider_PutObjectCondition(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		cond   storage.WriteCondition
		header string
		want   string
		name   string
	}{
		{name: "if-match is quoted", cond: storage.WriteCondition{IfMatch: new("abc")}, header: "If-Match", want: `"abc"`},
		{name: "a quoted if-match is sent once quoted", cond: storage.WriteCondition{IfMatch: new(`"abc"`)}, header: "If-Match", want: `"abc"`},
		{name: "if-none-match", cond: storage.WriteCondition{IfNoneMatch: new("*")}, header: "If-None-Match", want: "*"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"), Body: strings.NewReader("x"), WriteCondition: tt.cond,
			})
			if err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			if got := f.lastRequest(t).Header.Get(tt.header); got != tt.want {
				t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
			}
		})
	}

	t.Run("412 is ErrPreconditionFailed", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeS3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
		})
		_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			WriteCondition:  storage.WriteCondition{IfNoneMatch: new("*")},
		})
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			t.Errorf("PutObject() = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("an invalid condition sends nothing", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			WriteCondition:  storage.WriteCondition{IfNoneMatch: new("etag")},
		})
		if !errors.Is(err, storage.ErrInvalidCondition) || len(f.requests()) != 0 {
			t.Errorf("PutObject() = %v with %d requests, want ErrInvalidCondition and none",
				err, len(f.requests()))
		}
	})
}

// TestPresigner_PresignUploadURLCondition checks that a WriteCondition is
// signed, and that an invalid one signs nothing.
func TestPresigner_PresignUploadURLCondition(t *testing.T) {
	t.Parallel()
	p := newTestPresigner(t, newFakeS3(t, nil), testBucket)

	res, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{
			BaseObjectInput: base("k"),
			WriteCondition:  storage.WriteCondition{IfMatch: new("abc")},
		},
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	if !hasSignedHeader(parsePresigned(t, res), "if-match") ||
		res.SignedHeader.Get("If-Match") != `"abc"` {
		t.Errorf("If-Match not signed as \"abc\": SignedHeader = %v", res.SignedHeader)
	}

	res, err = p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{
			BaseObjectInput: base("k"),
			WriteCondition:  storage.WriteCondition{IfMatch: new("*")},
		},
	})
	if !errors.Is(err, storage.ErrInvalidCondition) || res != nil {
		t.Errorf("PresignUploadURL(IfMatch *) = %v, %v; want nil, ErrInvalidCondition", res, err)
	}
}
