// Package main demonstrates the gas/storage provider interface.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gasmod/gas"
	storage "github.com/gasmod/gas/storage"
)

// #region service
// A consuming service depends on the interface, never on a backend package,
// so it can be tested against storagetest.MockStorage.
type Service struct {
	storage gas.StorageProvider
}

func New(provider gas.StorageProvider) *Service {
	return &Service{storage: provider}
}

// #endregion service

// #region upload
func (s *Service) Upload(ctx context.Context, key, body string) error {
	return s.storage.Upload(ctx, key, strings.NewReader(body),
		gas.WithContentType("text/plain"),
		gas.WithMetadata(map[string]string{"source": "api"}),
	)
}

// #endregion upload

// #region download
func (s *Service) Read(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.storage.Download(ctx, key)
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil, gas.NotFound("file not found").WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close() //nolint:errcheck // documentation snippet

	buf := make([]byte, obj.Size)
	_, err = obj.Body.Read(buf)
	return buf, err
}

// #endregion download

// #region presign
// Presigned URLs let the browser transfer bytes straight to S3, so large
// files never pass through your server.
func (s *Service) UploadLink(ctx context.Context, key string) (string, error) {
	return s.storage.PresignUploadURL(ctx, key, 15*time.Minute,
		gas.WithContentType("image/png"),
	)
}

// #endregion presign

func main() { fmt.Println("documentation snippets") }
