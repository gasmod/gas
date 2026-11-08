package s3_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	s3store "github.com/gasmod/gas/storage/s3"
)

// replyStatuses are the statuses the fuzzer's replies take, by its input
// modulo their count.
var replyStatuses = []int{
	http.StatusOK, http.StatusPartialContent, http.StatusMovedPermanently,
	http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
	http.StatusPreconditionFailed, http.StatusRequestedRangeNotSatisfiable,
	http.StatusInternalServerError, http.StatusServiceUnavailable,
}

// Whatever a server replies, through the client Init builds and its
// limit on replies, a call returns without panicking: an error that
// starts with the operation, wraps a sentinel of the storage package and
// quotes nothing of the reply, or a result that keeps the contract.
func FuzzReply(f *testing.F) {
	for _, seed := range []string{
		"",
		`<Error><Code>zqx</Code><Message>zqx of /b-1/k</Message></Error>`,
		`<Error><Code>NoSuchKey</Code><Message>zqx</Message></Error>`,
		`<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`,
		`<ListBucketResult><Contents><Key>%zz</Key></Contents></ListBucketResult>`,
		`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>zqx` +
			`</NextContinuationToken><Contents><Key>a%20b</Key><Size>-1</Size></Contents>` +
			`</ListBucketResult>`,
		strings.Repeat("<", 4096),
	} {
		f.Add(uint8(0), seed)
		f.Add(uint8(3), seed)
	}

	fake := newFake(f)
	cfg := fake.config("b-1")

	cfg.Storage.MaxRetries = 0

	p, err := s3store.New(s3store.WithConfig(cfg))(nil, newRecorder())
	require.NoError(f, err)
	require.NoError(f, p.Init(f.Context()))
	f.Cleanup(func() { require.NoError(f, p.Close(context.Background())) })

	f.Fuzz(func(t *testing.T, status uint8, body string) {
		fake.setHook(rawReply(replyStatuses[int(status)%len(replyStatuses)], []byte(body)))

		list, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		if err == nil {
			require.Equal(t, list.IsTruncated, list.NextContinuationToken != "")
		}

		checkReplyError(t, err)

		obj, err := p.GetObject(t.Context(), &storage.GetObjectInput{
			BaseObjectInput: keyK,
		})
		if err == nil {
			_, err = io.ReadAll(obj.Body)
			require.NoError(t, errors.Join(err, obj.Body.Close()))
		}

		checkReplyError(t, err)
	})
}

// checkReplyError fails t unless err is nil, or an error of the store
// that wraps a sentinel of the storage package and holds nothing of the
// reply.
func checkReplyError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	require.True(t, strings.HasPrefix(err.Error(), s3store.ServiceName+": "), err.Error())
	require.NotContains(t, err.Error(), "zqx")

	for _, sentinel := range []error{
		storage.ErrBackend, storage.ErrAccessDenied, storage.ErrObjectNotFound,
		storage.ErrBucketNotFound, storage.ErrPreconditionFailed, storage.ErrRangeNotSatisfiable,
		storage.ErrBucketExists, storage.ErrBucketNotEmpty,
	} {
		if errors.Is(err, sentinel) {
			return
		}
	}

	t.Fatalf("%v wraps no sentinel", err)
}
