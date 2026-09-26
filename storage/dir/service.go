package dir

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

var (
	// ErrInvalidPath is returned when a provided file path is not a valid local file path.
	ErrInvalidPath = errors.New("storage/dir: invalid path")

	// ErrNoPresigner indicates that no presigner instance is available when a presign operation is attempted.
	ErrNoPresigner = errors.New("storage/dir: no presigner provided")
)

const (
	serviceName        = "gas/storage/dir"
	metaFilePrefix     = ".__GAS_FILE_METADATA__"
	metaFileExt        = ".json"
	tmpFilePrefix      = ".__GAS_FILE_TMP__"
	defaultContentType = "application/octet-stream"
	contentTypeMetaKey = "__GAS_METADATA_CONTENT_TYPE__"
)

// Service is an FS-backed storage implementing gas.Service and
// gas.StorageProvider.
type Service struct {
	logger gas.Logger

	root      *os.Root
	presigner Presigner

	closed atomic.Bool
}

var (
	_ gas.Service         = (*Service)(nil)
	_ gas.StorageProvider = (*Service)(nil)
	_ gas.ReadyReporter   = (*Service)(nil)
)

// Option configures a Service.
type Option func(*Service)

// WithRoot sets the root directory that objects are stored under. Each bucket
// is a directory directly under the root and must exist before use.
func WithRoot(root *os.Root) Option {
	return func(s *Service) { s.root = root }
}

// WithPresigner sets a custom Presigner for generating presigned URLs.
func WithPresigner(presigner Presigner) Option {
	return func(s *Service) { s.presigner = presigner }
}

// New captures options and returns a DI-injectable constructor.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service {
	return func(_ gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			logger: logger.With().Str("service", serviceName).Logger(),
		}

		for _, opt := range opts {
			opt(s)
		}

		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string {
	return serviceName
}

// Init checks that a root is configured and marks the service as open.
func (s *Service) Init() error {
	if s.root == nil {
		return fmt.Errorf("%s: %w", s.Name(), storage.ErrNotInitialized)
	}
	s.closed.Store(false)
	return nil
}

// Close marks the service as closed.
func (s *Service) Close() error {
	s.closed.Store(true)
	return nil
}

// Upload writes an object and its metadata under the bucket directory.
// Returns storage.ErrBucketNotFound if the bucket directory does not exist.
func (s *Service) Upload(
	ctx context.Context,
	key string,
	data io.Reader,
	opts ...gas.StorageOption,
) (err error) {
	if sErr := s.CheckReady(ctx); sErr != nil {
		return sErr
	}

	bucket, ct, meta := gas.ApplyStorageOptions(opts)
	bucket, err = s.resolveBucket(bucket)
	if err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, ErrInvalidPath)
	}

	if err = s.requireBucket("upload", bucket, key); err != nil {
		return err
	}

	name := filepath.Join(bucket, key)
	tmpName := filepath.Join(filepath.Dir(name), tmpFilePrefix+uuid.New().String())
	tmpMetaName := tmpName + metaFileExt

	committed := false
	defer func() {
		if committed {
			return
		}
		if rmErr := s.removeFiles(tmpName, tmpMetaName); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
	}()

	head, err := s.writeTemp(tmpName, data)
	if err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}

	meta = maps.Clone(s.resolveMetadata(meta))
	meta[contentTypeMetaKey] = s.resolveContentType(ct, head)

	if err = s.createFileMetadata(tmpMetaName, meta); err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}

	if err = s.commit(tmpName, tmpMetaName, name); err != nil {
		return fmt.Errorf("%s: upload %q: %w", s.Name(), key, err)
	}
	committed = true

	return nil
}

// Download opens an object for reading. The caller must close the returned Body.
// Returns storage.ErrKeyNotFound if the key does not exist, or
// storage.ErrBucketNotFound if the bucket directory does not exist.
func (s *Service) Download(
	ctx context.Context,
	key string,
	opts ...gas.StorageOption,
) (*gas.StorageObject, error) {
	if err := s.CheckReady(ctx); err != nil {
		return nil, err
	}

	bk, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bk)
	if err != nil {
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, ErrInvalidPath)
	}

	name := filepath.Join(bucket, key)
	f, fErr := s.root.Open(name)
	if fErr != nil {
		if errors.Is(fErr, fs.ErrNotExist) {
			if bErr := s.requireBucket("download", bucket, key); bErr != nil {
				return nil, bErr
			}
			return nil, storage.ErrKeyNotFound
		}
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, fErr)
	}

	stat, sErr := f.Stat()
	if sErr != nil {
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, closeWithErr(f, sErr))
	}

	if stat.IsDir() {
		return nil, closeWithErr(f, storage.ErrKeyNotFound)
	}

	meta, mErr := s.getFileMetadata(name)
	if mErr != nil {
		return nil, fmt.Errorf("%s: download %q: %w", s.Name(), key, closeWithErr(f, mErr))
	}

	meta = s.resolveMetadata(meta)
	ct := meta[contentTypeMetaKey]
	delete(meta, contentTypeMetaKey)

	return &gas.StorageObject{
		Body:        f,
		ContentType: ct,
		Size:        stat.Size(),
		Metadata:    meta,
	}, nil
}

// Delete removes an object and its metadata file. Deleting a missing key
// succeeds; returns storage.ErrBucketNotFound if the bucket directory does not
// exist.
func (s *Service) Delete(ctx context.Context, key string, opts ...gas.StorageOption) error {
	if err := s.CheckReady(ctx); err != nil {
		return err
	}

	bk, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bk)
	if err != nil {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, ErrInvalidPath)
	}

	if err = s.requireBucket("delete", bucket, key); err != nil {
		return err
	}

	name := filepath.Join(bucket, key)
	if err = s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, err)
	}

	if err = s.root.Remove(
		s.resolveMetadataFilename(name),
	); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: delete %q: %w", s.Name(), key, err)
	}

	return nil
}

// Head returns object metadata without opening the object for reading.
// Returns storage.ErrKeyNotFound if the key does not exist, or
// storage.ErrBucketNotFound if the bucket directory does not exist.
func (s *Service) Head(
	ctx context.Context,
	key string,
	opts ...gas.StorageOption,
) (*gas.ObjectInfo, error) {
	if err := s.CheckReady(ctx); err != nil {
		return nil, err
	}

	bk, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bk)
	if err != nil {
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, ErrInvalidPath)
	}

	name := filepath.Join(bucket, key)
	stat, sErr := s.root.Stat(name)
	if sErr != nil {
		if errors.Is(sErr, fs.ErrNotExist) {
			if bErr := s.requireBucket("head", bucket, key); bErr != nil {
				return nil, bErr
			}
			return nil, storage.ErrKeyNotFound
		}
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, sErr)
	}

	if stat.IsDir() {
		return nil, storage.ErrKeyNotFound
	}

	meta, mErr := s.getFileMetadata(name)
	if mErr != nil {
		return nil, fmt.Errorf("%s: head %q: %w", s.Name(), key, mErr)
	}

	meta = s.resolveMetadata(meta)
	ct := meta[contentTypeMetaKey]
	delete(meta, contentTypeMetaKey)

	return &gas.ObjectInfo{
		ContentType:  ct,
		Metadata:     meta,
		Size:         stat.Size(),
		LastModified: stat.ModTime(),
	}, nil
}

// PresignDownloadURL returns a download URL for the object from the configured Presigner.
func (s *Service) PresignDownloadURL(
	ctx context.Context,
	key string,
	ttl time.Duration,
	opts ...gas.StorageOption,
) (string, error) {
	if err := s.CheckReady(ctx); err != nil {
		return "", err
	}

	if s.presigner == nil {
		return "", fmt.Errorf("%s: presign download %q: %w", s.Name(), key, ErrNoPresigner)
	}

	bk, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bk)
	if err != nil {
		return "", fmt.Errorf("%s: presign download %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return "", fmt.Errorf("%s: presign download %q: %w", s.Name(), key, ErrInvalidPath)
	}

	url, uErr := s.presigner.PresignGetObject(bucket, key, ttl)
	if uErr != nil {
		return "", fmt.Errorf("%s: presign download %q: %w", s.Name(), key, uErr)
	}

	return url, nil
}

// PresignUploadURL returns an upload URL for the object from the configured Presigner.
func (s *Service) PresignUploadURL(
	ctx context.Context,
	key string,
	ttl time.Duration,
	opts ...gas.StorageOption,
) (string, error) {
	if err := s.CheckReady(ctx); err != nil {
		return "", err
	}

	if s.presigner == nil {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, ErrNoPresigner)
	}

	bk, _, _ := gas.ApplyStorageOptions(opts)
	bucket, err := s.resolveBucket(bk)
	if err != nil {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, err)
	}

	if !s.isValidPath(bucket, key) {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, ErrInvalidPath)
	}

	url, uErr := s.presigner.PresignPutObject(bucket, key, ttl)
	if uErr != nil {
		return "", fmt.Errorf("%s: presign upload %q: %w", s.Name(), key, uErr)
	}

	return url, nil
}

// CheckReady reports whether the service is open and has a root configured.
func (s *Service) CheckReady(context.Context) error {
	if s.closed.Load() {
		return storage.ErrClosed
	}
	if s.root == nil {
		return fmt.Errorf("%s: %w", s.Name(), storage.ErrNotInitialized)
	}
	return nil
}

func (s *Service) resolveBucket(bucket string) (string, error) {
	if bucket == "" {
		return "", storage.ErrBucketRequired
	}
	return bucket, nil
}

// requireBucket returns storage.ErrBucketNotFound if bucket's directory does
// not exist under the root. op and key only label other stat errors.
func (s *Service) requireBucket(op, bucket, key string) error {
	_, err := s.root.Stat(bucket)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return storage.ErrBucketNotFound
	default:
		return fmt.Errorf("%s: %s %q: %w", s.Name(), op, key, err)
	}
}

func (s *Service) resolveMetadata(meta map[string]string) map[string]string {
	if meta == nil {
		meta = make(map[string]string)
	}
	return meta
}

// commit renames the object, then its metadata, into place. A failure between
// the two renames leaves the new object paired with its previous metadata.
//
//nolint:wrapcheck // wrapped at the caller site
func (s *Service) commit(tmpName, tmpMetaName, name string) error {
	if err := s.root.Rename(tmpName, name); err != nil {
		return err
	}
	return s.root.Rename(tmpMetaName, s.resolveMetadataFilename(name))
}

// removeFiles removes the named files, ignoring any that do not exist.
func (s *Service) removeFiles(names ...string) error {
	var errs []error
	for _, name := range names {
		if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// closeWithErr closes f and joins any close error onto err.
func closeWithErr(f *os.File, err error) error {
	if cErr := f.Close(); cErr != nil {
		return errors.Join(err, cErr)
	}
	return err
}

// writeTemp creates name's parent directories, streams data into a new file at
// name and returns up to its first 512 bytes for content-type detection.
//
//nolint:wrapcheck // wrapped at the caller site
func (s *Service) writeTemp(name string, data io.Reader) (head []byte, err error) {
	if err = s.root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return nil, err
	}

	f, err := s.root.Create(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cErr := f.Close(); cErr != nil {
			err = errors.Join(err, cErr)
		}
	}()

	head = make([]byte, 512)
	n, err := io.ReadFull(data, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	head = head[:n]

	if _, err = io.Copy(f, io.MultiReader(bytes.NewReader(head), data)); err != nil {
		return nil, err
	}
	return head, nil
}

// resolveContentType returns ct when provided via WithContentType(), otherwise
// detects it from head. DetectContentType reports text/plain for empty input,
// so empty objects get the generic binary type instead.
func (s *Service) resolveContentType(ct string, head []byte) string {
	switch {
	case ct != "":
		return ct
	case len(head) == 0:
		return defaultContentType
	default:
		return http.DetectContentType(head)
	}
}

func (s *Service) resolveMetadataFilename(name string) string {
	return filepath.Join(filepath.Dir(name), metaFilePrefix+filepath.Base(name)+metaFileExt)
}

func (s *Service) isValidPath(bucket, key string) bool {
	if strings.ContainsAny(bucket, `/\`) || !filepath.IsLocal(bucket) || !filepath.IsLocal(key) {
		return false
	}
	name := filepath.Base(key)
	if strings.HasPrefix(name, metaFilePrefix) && strings.HasSuffix(name, metaFileExt) {
		return false
	}
	if strings.HasPrefix(name, tmpFilePrefix) {
		return false
	}
	return true
}

//nolint:wrapcheck // wrapped at the caller site
func (s *Service) createFileMetadata(path string, meta map[string]string) error {
	m, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.root.WriteFile(path, m, 0o644)
}

//nolint:wrapcheck // wrapped at the caller site
func (s *Service) getFileMetadata(name string) (map[string]string, error) {
	f, err := s.root.ReadFile(s.resolveMetadataFilename(name))
	if err != nil {
		return nil, err
	}
	var meta map[string]string
	if jsErr := json.Unmarshal(f, &meta); jsErr != nil {
		return nil, jsErr
	}
	return meta, nil
}
