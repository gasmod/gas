package dir_test

import (
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// A copy of an object onto itself with Replace closes the data file it
// reads before it removes it, so that, on Windows too, it leaves one data
// file, logs no warning and passes nothing to the skip handler.
func TestCopyObject_OntoItselfLeavesOneDataFile(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	got := &skips{}
	p, log := build(t, dir.WithConfig(config(path, bucket)), dir.WithSkipHandler(got.handler()))
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(t.Context())) }()

	put(t, p, "k", "Tom")

	out, err := p.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:      "k",
		Metadata: map[string]string{"n": "v"},
		Source:   storage.BaseObjectInput{Key: "k"},
		Replace:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), out.Size)
	assert.Equal(t, "Tom", read(t, p, "k"))
	assert.Len(t, dataFiles(t, path, "k"), 1)
	assert.Empty(t, got.all())

	for _, e := range log.entries() {
		assert.NotEqual(t, "warn", e.level, e.msg)
	}
}

// A source longer than storage.MaxCopySize is refused before the
// destination is written. The data file is made that long sparsely, which
// a Windows file system would allocate, so the test runs elsewhere.
func TestCopyObject_SourceTooLong(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("NTFS allocates a file extended without the sparse attribute")
	}

	p, path := open(t)

	put(t, p, "k", "Tom")

	files := dataFiles(t, path, "k")
	require.Len(t, files, 1)
	require.NoError(t, os.Truncate(files[0], storage.MaxCopySize+1))

	_, err := p.CopyObject(t.Context(), &storage.CopyObjectInput{
		Key:    "dst",
		Source: storage.BaseObjectInput{Key: "k"},
	})
	require.ErrorIs(t, err, storage.ErrInvalidArgument)
	require.EqualError(t, err,
		"gas/storage/dir: copy object: invalid argument: source is longer than 5368709120 bytes")

	_, err = p.HeadObject(t.Context(), headIn("dst"))
	require.ErrorIs(t, err, storage.ErrObjectNotFound)
}
