package conformance_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// signerName is the fake presigner's service name.
const signerName = "gas/storage/fake-presigner"

// signed is what a fake presigned URL allows, sealed in its query.
type signed struct {
	// Metadata is the metadata of an upload.
	Metadata map[string]string `json:"metadata,omitempty"`
	// ContentType is the Content-Type of an upload, or the response's of
	// a download.
	ContentType string `json:"contentType"`
	// ContentDisposition is the Content-Disposition of a download's
	// response.
	ContentDisposition string `json:"contentDisposition"`
	// Expires is when the URL stops being valid.
	Expires time.Time `json:"expires"`
	// Method is the HTTP method.
	Method string `json:"method"`
	// Bucket is the bucket of the object.
	Bucket string `json:"bucket"`
	// Key is the key of the object.
	Key string `json:"key"`
	// Range is the Range of a download, or empty.
	Range string `json:"range,omitempty"`
	// Length is the length of an upload's body.
	Length int64 `json:"length"`
}

// fakeSigner is a storage.Presigner whose URLs point at a server in
// front of a store, and carry what they allow sealed with an HMAC. It
// keeps the rules of the storage package with its checks and lifecycle,
// so that the conformance suite's presign rules can run without S3.
type fakeSigner struct {
	// store holds the objects the URLs upload and download.
	store storage.Provider
	// srv serves the URLs.
	srv *httptest.Server
	// key signs the URLs.
	key []byte
	// bucket is the configured bucket.
	bucket string
	// lc is the lifecycle, whose sessions hold the configured bucket.
	lc lifecycle.Lifecycle[string]
}

var (
	_ gas.Service       = (*fakeSigner)(nil)
	_ storage.Presigner = (*fakeSigner)(nil)
)

// newFakeSigner returns a fakeSigner of store, whose server stops when t
// ends.
func newFakeSigner(t *testing.T, store storage.Provider, bucket string) *fakeSigner {
	t.Helper()

	f := &fakeSigner{store: store, key: []byte("fake signing key"), bucket: bucket}

	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

// Name returns signerName.
func (*fakeSigner) Name() string { return signerName }

// Init opens the presigner.
func (f *fakeSigner) Init(ctx context.Context) error {
	err := f.lc.Init(ctx, func() (string, error) { return f.bucket, nil })
	if err != nil {
		return fmt.Errorf("%s: init: %w", signerName, err)
	}

	return nil
}

// Close closes the presigner.
func (f *fakeSigner) Close(ctx context.Context) error {
	_, err := f.lc.Close(ctx)
	if err != nil {
		return fmt.Errorf("%s: close: %w", signerName, err)
	}

	return nil
}

// PresignUploadURL signs an upload of in.
func (f *fakeSigner) PresignUploadURL(
	ctx context.Context,
	in *storage.PresignUploadURLInput,
) (*storage.PresignURLResponse, error) {
	const op = "presign upload"

	if in == nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, storage.ErrNilInput)
	}

	sess, err := f.lc.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, err)
	}
	defer sess.End()

	req, err := storage.CheckPresignUpload(in, sess.Value)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, err)
	}

	resp := f.sign(&signed{
		Metadata:    req.Metadata,
		ContentType: req.ContentType,
		Expires:     time.Now().Add(req.TTL),
		Method:      http.MethodPut,
		Bucket:      req.Bucket,
		Key:         req.Key,
		Length:      req.ContentLength,
	})

	resp.SignedHeader.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	resp.SignedHeader.Set("Content-Type", req.ContentType)

	return resp, nil
}

// PresignDownloadURL signs a download of in.
func (f *fakeSigner) PresignDownloadURL(
	ctx context.Context,
	in *storage.PresignDownloadURLInput,
) (*storage.PresignURLResponse, error) {
	const op = "presign download"

	if in == nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, storage.ErrNilInput)
	}

	sess, err := f.lc.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, err)
	}
	defer sess.End()

	req, err := storage.CheckPresignDownload(in, sess.Value)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", signerName, op, err)
	}

	s := &signed{
		ContentType:        req.Response.ContentType,
		ContentDisposition: req.Response.ContentDisposition,
		Expires:            time.Now().Add(req.TTL),
		Method:             http.MethodGet,
		Bucket:             req.Bucket,
		Key:                req.Key,
	}
	if req.Range != nil {
		s.Range = req.Range.String()
	}

	return f.sign(s), nil
}

// sign returns the presigned request of s.
func (f *fakeSigner) sign(s *signed) *storage.PresignURLResponse {
	payload, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}

	token := base64.RawURLEncoding.EncodeToString(payload)

	return &storage.PresignURLResponse{
		SignedHeader: http.Header{},
		Expires:      s.Expires,
		URL:          f.srv.URL + "/?t=" + token + "&sig=" + f.mac(token),
		Method:       s.Method,
	}
}

// mac returns the signature of token.
func (f *fakeSigner) mac(token string) string {
	m := hmac.New(sha256.New, f.key)
	m.Write([]byte(token))

	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// errRefused is a request the server refuses.
var errRefused = errors.New("refused")

// open returns what the request r is signed for, or errRefused.
func (f *fakeSigner) open(r *http.Request) (*signed, error) {
	token := r.URL.Query().Get("t")
	if !hmac.Equal([]byte(f.mac(token)), []byte(r.URL.Query().Get("sig"))) {
		return nil, errRefused
	}

	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errRefused
	}

	var s signed

	err = json.Unmarshal(payload, &s)
	if err != nil || time.Now().After(s.Expires) || r.Method != s.Method {
		return nil, errRefused
	}

	if s.Method == http.MethodPut &&
		(r.ContentLength != s.Length || r.Header.Get("Content-Type") != s.ContentType) {
		return nil, errRefused
	}

	return &s, nil
}

// serve answers a presigned request.
func (f *fakeSigner) serve(w http.ResponseWriter, r *http.Request) {
	s, err := f.open(r)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)

		return
	}

	if s.Method == http.MethodPut {
		f.upload(w, r, s)

		return
	}

	f.download(w, r, s)
}

// upload stores the body of r as s allows.
func (f *fakeSigner) upload(w http.ResponseWriter, r *http.Request, s *signed) {
	in := &storage.PutObjectInput{
		Bucket: &s.Bucket, Key: s.Key,
		Body:          r.Body,
		ContentLength: &s.Length,
	}

	in.Metadata = s.Metadata
	in.ContentType = &s.ContentType

	_, err := f.store.PutObject(r.Context(), in)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	w.WriteHeader(http.StatusOK)
}

// download writes the object s allows, with its response header fields.
func (f *fakeSigner) download(w http.ResponseWriter, r *http.Request, s *signed) {
	in := &storage.GetObjectInput{
		Bucket: &s.Bucket, Key: s.Key,
	}
	if s.Range != "" {
		in.Range = &s.Range
	}

	obj, err := f.store.GetObject(r.Context(), in)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	// A memory Body's Close does nothing and returns nil.
	defer func() { _ = obj.Body.Close() }()

	h := obj.HeaderValues

	for name, v := range map[string]string{
		"Content-Type":        s.ContentType,
		"Content-Disposition": s.ContentDisposition,
	} {
		if v != "" {
			w.Header().Set(name, v)
		}
	}

	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", h.ContentType)
	}

	w.WriteHeader(http.StatusOK)

	// An error here is a client that went away, with no one to tell.
	_, _ = io.Copy(w, obj.Body)
}
