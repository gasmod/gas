package storage

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestInputFieldPromotion checks that embedded input fields are promoted.
func TestInputFieldPromotion(t *testing.T) {
	t.Parallel()

	bucket, key, ver := "b", "k", "v"
	ttl := time.Minute

	get := GetObjectInput{HeadObjectInput: HeadObjectInput{
		BaseObjectInput: BaseObjectInput{Bucket: &bucket, Key: key},
		Version:         &ver,
	}}
	if get.Bucket != &bucket || get.Key != key || get.Version != &ver {
		t.Error("GetObjectInput does not promote HeadObjectInput/BaseObjectInput fields")
	}

	dl := PresignDownloadURLInput{GetObjectInput: get, TTL: &ttl}
	if dl.Key != key || dl.Version != &ver || dl.TTL != &ttl {
		t.Error("PresignDownloadURLInput does not promote GetObjectInput fields")
	}

	ul := PresignUploadURLInput{PutObjectInput: PutObjectInput{
		BaseObjectInput: BaseObjectInput{Key: key},
		Body:            strings.NewReader("x"),
	}, TTL: &ttl}
	if ul.Key != key || ul.Body == nil || ul.Bucket != nil {
		t.Error("PresignUploadURLInput does not promote PutObjectInput fields")
	}

	del := DeleteObjectInput{BaseObjectInput: BaseObjectInput{Key: key}, Version: &ver}
	if del.Key != key || del.Version != &ver {
		t.Error("DeleteObjectInput fields not set")
	}
}

// TestObjectEmbedsInfo checks that Object exposes ObjectInfo fields and its
// Body.
func TestObjectEmbedsInfo(t *testing.T) {
	t.Parallel()

	now := time.Now()
	obj := Object{
		Body: io.NopCloser(strings.NewReader("data")),
		ObjectInfo: ObjectInfo{
			LastModified: now,
			Metadata:     map[string]string{"a": "b"},
			ContentType:  "text/plain",
			Version:      "v1",
			Size:         4,
		},
	}
	if obj.Size != 4 || obj.ContentType != "text/plain" || obj.Version != "v1" ||
		!obj.LastModified.Equal(now) || obj.Metadata["a"] != "b" {
		t.Errorf("Object does not expose ObjectInfo fields: %+v", obj)
	}
	data, err := io.ReadAll(obj.Body)
	if err != nil || string(data) != "data" {
		t.Errorf("Body = %q, %v", data, err)
	}
	if err := obj.Body.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

// TestZeroValues checks the zero values of ObjectInfo and PresignURLResponse.
func TestZeroValues(t *testing.T) {
	t.Parallel()

	var info ObjectInfo
	if info.Size != 0 || info.Metadata != nil || !info.LastModified.IsZero() {
		t.Error("zero ObjectInfo is not zero")
	}
	var res PresignURLResponse
	if res.SignedHeader != nil || res.URL != "" || res.Method != "" {
		t.Error("zero PresignURLResponse is not zero")
	}
}
