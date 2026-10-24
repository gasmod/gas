// Package main shows gas/storage wiring and the storage.Provider and
// storage.Presigner interfaces.
package main

// #region imports
import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	storages3 "github.com/gasmod/gas/storage/s3"
)

// #endregion imports

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		// Both s3 constructors resolve the ConfigProvider and bind from it.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),

		// Registered under the interfaces services inject.
		gas.WithSingletonService[storage.Provider](storages3.NewProvider()),
		gas.WithSingletonService[storage.Presigner](storages3.NewPresigner()),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// runWithDir serves an App whose storage.Provider is a local directory.
func runWithDir(cfg gas.ConfigProvider) (err error) {
	// #region dir
	// The provider never closes the root; whoever opens it does.
	root, err := os.OpenRoot("/var/lib/myapp/storage")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	app := gas.NewApp(
		// The constructor binds storage.bucket from the ConfigProvider, so
		// one must be registered even when nothing is configured.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[storage.Provider](
			storagedir.NewProvider(storagedir.WithRoot(root)),
		),
	)
	// #endregion dir

	return app.Run()
}

// #region service
// Files depends on the interfaces, never on a backend package, so it runs
// unchanged on S3 or a directory and can be tested against the
// storagetest mocks.
type Files struct {
	storage   storage.Provider
	presigner storage.Presigner
}

func NewFiles(provider storage.Provider, presigner storage.Presigner) *Files {
	return &Files{storage: provider, presigner: presigner}
}

// #endregion service

// #region upload
// Upload writes body under key in the configured bucket, the one
// storage.bucket names, because the input leaves Bucket nil.
func (f *Files) Upload(ctx context.Context, key, body string) (etag string, err error) {
	contentType := "text/plain; charset=utf-8"
	size := int64(len(body))

	info, err := f.storage.PutObject(ctx, &storage.PutObjectInput{
		Body:          strings.NewReader(body),
		ContentLength: &size,
		ObjectHeaders: storage.ObjectHeaders{
			ContentType: &contentType,
			Metadata:    map[string]string{"source": "api"},
		},
		BaseObjectInput: storage.BaseObjectInput{Key: key},
	})
	if err != nil {
		return "", err
	}
	return info.ETag, nil
}

// #endregion upload

// #region download
func (f *Files) Read(ctx context.Context, key string) (data []byte, err error) {
	obj, err := f.storage.GetObject(ctx, &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput: storage.BaseObjectInput{Key: key},
		},
	})
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil, gas.NotFound("file not found").WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	// The caller owns Body and must close it.
	defer func() { err = errors.Join(err, obj.Body.Close()) }()

	return io.ReadAll(obj.Body)
}

// #endregion download

// #region conditional
// Append adds a line to a text object without losing a concurrent writer's
// update: the write only lands over the exact version that was read.
func (f *Files) Append(ctx context.Context, key, line string) error {
	current, err := f.Read(ctx, key)
	if err != nil {
		return err
	}
	head, err := f.storage.HeadObject(ctx, &storage.HeadObjectInput{
		BaseObjectInput: storage.BaseObjectInput{Key: key},
	})
	if err != nil {
		return err
	}

	_, err = f.storage.PutObject(ctx, &storage.PutObjectInput{
		Body:            strings.NewReader(string(current) + line + "\n"),
		WriteCondition:  storage.WriteCondition{IfMatch: &head.ETag},
		BaseObjectInput: storage.BaseObjectInput{Key: key},
	})
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return gas.Conflict("file changed, retry").WithCause(err)
	}
	return err
}

// #endregion conditional

// #region list
// Keys returns every key under prefix, one page at a time.
func (f *Files) Keys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	in := &storage.ListObjectsInput{Prefix: &prefix}
	for {
		out, err := f.storage.ListObjects(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, obj := range out.Objects {
			keys = append(keys, obj.Key)
		}
		if !out.IsTruncated {
			return keys, nil
		}
		in.ContinuationToken = &out.NextContinuationToken
	}
}

// #endregion list

// #region presign
// UploadLink lets a browser upload a PNG straight to S3, so large files
// never pass through the app. The client must send the returned method and
// every signed header.
func (f *Files) UploadLink(ctx context.Context, key string) (*storage.PresignURLResponse, error) {
	ttl := 10 * time.Minute
	contentType := "image/png"
	return f.presigner.PresignUploadURL(ctx, &storage.PresignUploadURLInput{
		TTL: &ttl,
		PutObjectInput: storage.PutObjectInput{
			ObjectHeaders:   storage.ObjectHeaders{ContentType: &contentType},
			BaseObjectInput: storage.BaseObjectInput{Key: key},
		},
	})
}

// #endregion presign
