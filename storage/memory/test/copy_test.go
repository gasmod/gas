package memory_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/memory"
)

// A copy counts toward MaxBytes and MaxObjects as an object of its own,
// though it shares the source's contents; one past a limit is
// ErrStorageFull and writes nothing, and one that replaces an object
// counts as an object no more.
func TestCopyObject_Limits(t *testing.T) {
	t.Parallel()

	cfg := withBucket("photos")

	cfg.Storage.MaxBytes = memory.MinMaxBytes
	cfg.Storage.MaxObjects = 3

	p := open(t, cfg)
	ctx := t.Context()
	half := strings.Repeat("v", memory.MinMaxBytes/2)
	copyTo := func(src, dst string) error {
		_, err := p.CopyObject(ctx, &storage.CopyObjectInput{
			Key:    dst,
			Source: storage.BaseObjectInput{Key: src},
		})

		return err
	}

	_, err := p.PutObject(ctx, &storage.PutObjectInput{Key: "a", Body: strings.NewReader(half)})
	require.NoError(t, err)

	err = copyTo("a", "b")
	require.ErrorIs(t, err, memory.ErrStorageFull)
	require.EqualError(t, err, "gas/storage/memory: copy object: storage full")

	_, err = p.HeadObject(ctx, &storage.HeadObjectInput{Key: "b"})
	require.ErrorIs(t, err, storage.ErrObjectNotFound)

	_, err = p.PutObject(ctx, &storage.PutObjectInput{Key: "s", Body: strings.NewReader("x")})
	require.NoError(t, err)
	require.NoError(t, copyTo("s", "t"))
	require.ErrorIs(t, copyTo("s", "u"), memory.ErrStorageFull)
	require.ErrorIs(t, copyTo("a", "t"), memory.ErrStorageFull)
	require.NoError(t, copyTo("s", "t"))
}
