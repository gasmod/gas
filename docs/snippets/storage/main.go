// Package main demonstrates the gas/storage provider interfaces.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// #region service
// A consuming service depends on the interfaces, never on a backend package,
// so it can be tested against storagetest.MockProvider and
// storagetest.MockPresigner.
type Service struct {
	storage   storage.Provider
	presigner storage.Presigner
}

func New(provider storage.Provider, presigner storage.Presigner) *Service {
	return &Service{storage: provider, presigner: presigner}
}

// #endregion service

// #region upload
func (s *Service) Upload(ctx context.Context, key, body string) error {
	contentType := "text/plain"
	_, err := s.storage.PutObject(ctx, &storage.PutObjectInput{
		Body:            strings.NewReader(body),
		ContentType:     &contentType,
		Metadata:        map[string]string{"source": "api"},
		BaseObjectInput: storage.BaseObjectInput{Key: key},
	})
	return err
}

// #endregion upload

// #region download
func (s *Service) Read(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.storage.GetObject(ctx, &storage.GetObjectInput{
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
	defer obj.Body.Close() //nolint:errcheck // documentation snippet

	return io.ReadAll(obj.Body)
}

// #endregion download

// #region presign
// Presigned URLs let the browser transfer bytes straight to S3, so large
// files never pass through your server. The client must send the returned
// method and signed headers.
func (s *Service) UploadLink(ctx context.Context, key string) (*storage.PresignURLResponse, error) {
	ttl := 15 * time.Minute
	contentType := "image/png"
	return s.presigner.PresignUploadURL(ctx, &storage.PresignUploadURLInput{
		TTL: &ttl,
		PutObjectInput: storage.PutObjectInput{
			ContentType:     &contentType,
			BaseObjectInput: storage.BaseObjectInput{Key: key},
		},
	})
}

// #endregion presign

// runWithDir serves an App whose storage.Provider is a local directory.
func runWithDir(cfg gas.ConfigProvider) error {
	// #region dir
	root, err := os.OpenRoot("/var/lib/myapp/storage")
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // documentation snippet

	app := gas.NewApp(
		// The provider binds its settings from the ConfigProvider, so one
		// must be registered even when nothing is configured.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[storage.Provider](storagedir.NewProvider(storagedir.WithRoot(root))),
	)
	// #endregion dir

	return app.Run()
}

func main() { fmt.Println("documentation snippets") }
