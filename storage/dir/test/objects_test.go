package dir_test

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// A write stores a metadata file holding the key, the header fields, the
// metadata with names in lower case, the ETag and the data file, a data
// file of the contents, and an entry in the key index.
func TestPutObject_Layout(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	in := putIn("cats/tom.txt", "Tom")

	in.Metadata = map[string]string{"Author": "Ann"}

	out, err := p.PutObject(t.Context(), in)
	require.NoError(t, err)

	raw, err := os.ReadFile(metaFile(path, "cats/tom.txt"))
	require.NoError(t, err)

	var meta map[string]any

	require.NoError(t, json.Unmarshal(raw, &meta))

	files := dataFiles(t, path, "cats/tom.txt")
	require.Len(t, files, 1)
	assert.Equal(t, map[string]any{
		"key":         "cats/tom.txt",
		"etag":        out.ETag,
		"dataFile":    filepath.Base(files[0]),
		"metadata":    map[string]any{"author": "Ann"},
		"contentType": storage.DefaultContentType,
	}, meta)

	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Equal(t, "Tom", string(data))
	assert.FileExists(
		t,
		filepath.Join(keysDir(path), hex.EncodeToString([]byte("cats/tom.txt"))+".k"),
	)
}

// A failed write leaves no data file, no index entry and no lock file of
// the key it would have created.
func TestPutObject_FailureLeavesNothing(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	in := putIn("k", "")

	in.Body = io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(io.ErrUnexpectedEOF))

	_, err := p.PutObject(t.Context(), in)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)

	assert.Empty(t, dataFiles(t, path, "k"))
	assert.NoFileExists(
		t,
		filepath.Join(keysDir(path), hex.EncodeToString([]byte("k"))+".k"),
	)
	assert.NoFileExists(t, filepath.Join(locksDir(path, bucket), sha("k")))
	assert.NoFileExists(t, metaFile(path, "k"))
}

// An overwrite replaces the data file, so one data file stays per object.
func TestPutObject_OverwriteRemovesOldData(t *testing.T) {
	t.Parallel()

	p, path := open(t)

	for _, body := range []string{"one", "two", "three"} {
		put(t, p, "k", body)
	}

	assert.Len(t, dataFiles(t, path, "k"), 1)
	assert.Equal(t, "three", read(t, p, "k"))
}

// A data file of the object replaced that cannot be removed does not fail
// the write: the skip handler gets it and a warning is logged.
func TestPutObject_StaleData(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p, log := build(t, dir.WithConfig(config(path, bucket)), dir.WithSkipHandler(got.handler()))
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(t.Context())) }()

	put(t, p, "k", "old")

	// A non-empty directory where the old data file was: it cannot be
	// removed as a file is.
	old := dataFiles(t, path, "k")[0]
	require.NoError(t, os.Remove(old))
	require.NoError(t, os.MkdirAll(filepath.Join(old, "x"), 0o750))

	put(t, p, "k", "new")
	assert.Equal(t, "new", read(t, p, "k"))

	all := got.all()
	require.Len(t, all, 1)
	assert.Equal(t, filepath.Join(".gas", "objects", sha(bucket), filepath.Base(old)), all[0].path)
	require.ErrorContains(t, all[0].err, "data file of the object replaced left in place")

	e, ok := log.find("put object: data file of the object replaced left in place")
	require.True(t, ok)
	assert.Equal(t, "warn", e.level)
}

// GetObject's Body is an io.ReadSeeker; a ranged Body seeks within the
// bytes selected.
func TestGetObject_BodySeeks(t *testing.T) {
	t.Parallel()

	p, _ := open(t)
	put(t, p, "k", "0123456789")

	whole, err := p.GetObject(t.Context(), getIn("k"))
	require.NoError(t, err)

	defer func() { require.NoError(t, whole.Body.Close()) }()

	rs, ok := whole.Body.(io.ReadSeeker)
	require.True(t, ok)

	_, err = rs.Seek(7, io.SeekStart)
	require.NoError(t, err)

	b, err := io.ReadAll(rs)
	require.NoError(t, err)
	assert.Equal(t, "789", string(b))

	in := getIn("k")

	in.Range = new("bytes=2-5")

	ranged, err := p.GetObject(t.Context(), in)
	require.NoError(t, err)

	defer func() { require.NoError(t, ranged.Body.Close()) }()

	rs, ok = ranged.Body.(io.ReadSeeker)
	require.True(t, ok)

	_, err = rs.Seek(-2, io.SeekEnd)
	require.NoError(t, err)

	b, err = io.ReadAll(rs)
	require.NoError(t, err)
	assert.Equal(t, "45", string(b))
}

// A Body keeps reading the contents it opened when the object is replaced
// and deleted, but on Windows.
func TestGetObject_BodyOutlivesWrites(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("an open file cannot be removed on Windows")
	}

	p, _ := open(t)
	put(t, p, "k", "first")

	obj, err := p.GetObject(t.Context(), getIn("k"))
	require.NoError(t, err)

	put(t, p, "k", "second")
	require.NoError(t, p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
		Key: "k",
	}))

	b, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.NoError(t, obj.Body.Close())
	assert.Equal(t, "first", string(b))
}

// corruptions damage the object `k` of the directory path, each in its own
// way that no read can use.
var corruptions = map[string]func(t *testing.T, path string){
	"not JSON": func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.WriteFile(metaFile(path, "k"), []byte("{"), 0o600))
	},
	"null": func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.WriteFile(metaFile(path, "k"), []byte("null"), 0o600))
	},
	"wrong type": func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.WriteFile(metaFile(path, "k"), []byte(`{"key": 1}`), 0o600))
	},
	"another key": func(t *testing.T, path string) {
		t.Helper()
		editMeta(t, path, func(m map[string]any) { m["key"] = "other" })
	},
	"a data file outside": func(t *testing.T, path string) {
		t.Helper()
		editMeta(t, path, func(m map[string]any) { m["dataFile"] = "../../../etc/passwd" })
	},
	"another key's data file": func(t *testing.T, path string) {
		t.Helper()
		editMeta(t, path, func(m map[string]any) {
			m["dataFile"] = sha("other") + "_00000000-0000-4000-8000-000000000000"
		})
	},
	"a directory as metadata": func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.Remove(metaFile(path, "k")))
		require.NoError(t, os.Mkdir(metaFile(path, "k"), 0o750))
	},
	"a directory as data file": func(t *testing.T, path string) {
		t.Helper()

		f := dataFiles(t, path, "k")[0]
		require.NoError(t, os.Remove(f))
		require.NoError(t, os.Mkdir(f, 0o750))
	},
}

// editMeta rewrites the metadata file of `k` with edit applied.
func editMeta(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()

	raw, err := os.ReadFile(metaFile(path, "k"))
	require.NoError(t, err)

	var m map[string]any

	require.NoError(t, json.Unmarshal(raw, &m))
	edit(m)

	raw, err = json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metaFile(path, "k"), raw, 0o600))
}

// requireCorrupt fails t unless HeadObject and GetObject of `k`, and
// DeleteObject when del is set, return errors wrapping ErrCorrupt and
// storage.ErrBackend, not storage.ErrObjectNotFound, that hold no path.
func requireCorrupt(t *testing.T, p *dir.Provider, path string, del bool) {
	t.Helper()

	_, headErr := p.HeadObject(t.Context(), headIn("k"))
	_, getErr := p.GetObject(t.Context(), getIn("k"))

	errs := []error{headErr, getErr}
	if del {
		errs = append(errs, p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
			Key: "k",
		}))
	}

	for _, err := range errs {
		require.ErrorIs(t, err, dir.ErrCorrupt)
		require.ErrorIs(t, err, storage.ErrBackend)
		require.NotErrorIs(t, err, storage.ErrObjectNotFound)
		requireClean(t, err, path)
	}
}

// An object no read can use is ErrCorrupt and storage.ErrBackend, never
// storage.ErrObjectNotFound, from HeadObject and GetObject, and from
// DeleteObject, which deletes nothing, when its metadata cannot be used;
// the error holds no path; a listing leaves it out and reports it; a
// write with a condition fails, and one without replaces it, but a
// directory in place of its metadata file.
func TestObject_Corrupt(t *testing.T) {
	t.Parallel()

	for name, corrupt := range corruptions {
		dataOnly := name == "a directory as data file"
		metaDir := name == "a directory as metadata"

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got skips

			path := t.TempDir()
			p := openAt(t, path, dir.WithSkipHandler(got.handler()))
			ctx := t.Context()

			put(t, p, "k", "v")
			corrupt(t, path)

			requireCorrupt(t, p, path, !dataOnly)

			out, err := p.ListObjects(ctx, &storage.ListObjectsInput{})
			require.NoError(t, err)
			assert.Empty(t, out.Objects)

			all := got.all()
			require.Len(t, all, 1)
			assert.Equal(t, filepath.Join(sha(bucket), sha("k")+".gas.json"), all[0].path)
			require.ErrorIs(t, all[0].err, dir.ErrCorrupt)

			for _, c := range []storage.WriteCondition{{IfNoneMatch: new("*")}, {IfMatch: new("abc")}} {
				in := putIn("k", "w")

				in.WriteCondition = c

				_, err = p.PutObject(ctx, in)
				require.ErrorIs(t, err, storage.ErrPreconditionFailed)
			}

			if metaDir {
				_, err = p.PutObject(ctx, putIn("k", "w"))
				require.ErrorIs(t, err, storage.ErrBackend)

				return
			}

			put(t, p, "k", "repaired")
			assert.Equal(t, "repaired", read(t, p, "k"))
		})
	}
}
