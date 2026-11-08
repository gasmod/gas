package vfs_test

import (
	"crypto/md5" //nolint:gosec // G501: the ETag under test is an MD5.
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// md5Hex returns the hex MD5 of s.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // G401: an ETag.

	return hex.EncodeToString(sum[:])
}

// An object reads back with its contents, header fields, metadata, ETag,
// size and modification time, from OpenObject and Stat.
func TestCreateObject_RoundTrip(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)
	w := write("cats/tom.txt", "Tom")

	w.Headers = vfs.Headers{
		ContentType:  "text/plain",
		CacheControl: "no-cache",
		Expires:      "Wed, 21 Oct 2015 07:28:00 GMT",
	}
	w.Metadata = map[string]string{"author": "ann"}

	before := time.Now().Add(-time.Second)

	out, err := bk.CreateObject(t.Context(), w)
	require.NoError(t, err)
	assert.Equal(t, md5Hex("Tom"), out.ETag)
	assert.Equal(t, int64(3), out.Size)
	assert.Empty(t, out.Stale)

	obj, err := bk.OpenObject(t.Context(), "cats/tom.txt")
	require.NoError(t, err)

	body, err := io.ReadAll(obj.File)
	require.NoError(t, err)
	require.NoError(t, obj.File.Close())

	st, err := bk.Stat(t.Context(), "cats/tom.txt")
	require.NoError(t, err)

	for _, s := range []vfs.ObjectStat{obj.ObjectStat, st} {
		assert.Equal(t, w.Headers, s.Headers)
		assert.Equal(t, w.Metadata, s.User)
		assert.Equal(t, out.ETag, s.ETag)
		assert.Equal(t, int64(3), s.Size)
		assert.Equal(t, time.UTC, s.LastModified.Location())
		assert.True(t, s.LastModified.After(before))
	}

	assert.Equal(t, "Tom", string(body))
}

// An overwrite replaces the data file, and a reader of the old object
// keeps reading the contents it opened.
func TestCreateObject_Overwrite(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "old")

	obj, err := bk.OpenObject(t.Context(), "k")
	require.NoError(t, err)

	put(t, bk, "k", "new")
	assert.Len(t, dataFiles(t, dir, "k"), 1)
	assert.Equal(t, "new", read(t, bk, "k"))

	b, err := io.ReadAll(obj.File)
	require.NoError(t, err)
	require.NoError(t, obj.File.Close())
	assert.Equal(t, "old", string(b))
}

// IfNoneMatch writes only a key with no object, IfMatch only over the
// object with that ETag, and IfMatch of a key with no object is
// ErrObjectNotFound; a failed condition writes nothing.
func TestCreateObject_Conditions(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	w := write("k", "v1")

	w.IfMatch = md5Hex("v0")

	_, err := bk.CreateObject(t.Context(), w)
	require.ErrorIs(t, err, vfs.ErrObjectNotFound)
	assert.Empty(t, dataFiles(t, dir, "k"))
	assert.Empty(t, listKeys(t, bk, vfs.Bounds{}))
	assert.NoFileExists(t, filepath.Join(lockDir(dir), hash("k")))

	w = write("k", "v1")
	w.IfNoneMatch = true
	_, err = bk.CreateObject(t.Context(), w)
	require.NoError(t, err)

	for _, c := range []*vfs.Write{
		{IfNoneMatch: true},
		{IfMatch: md5Hex("other")},
	} {
		w = write("k", "v2")
		w.IfMatch, w.IfNoneMatch = c.IfMatch, c.IfNoneMatch
		_, err = bk.CreateObject(t.Context(), w)
		require.ErrorIs(t, err, vfs.ErrPreconditionFailed)
	}

	assert.Len(t, dataFiles(t, dir, "k"), 1)

	w = write("k", "v2")
	w.IfMatch = md5Hex("v1")
	_, err = bk.CreateObject(t.Context(), w)
	require.NoError(t, err)
	assert.Equal(t, "v2", read(t, bk, "k"))
}

// Of writes with IfNoneMatch that race on one key, one wins.
func TestCreateObject_IfNoneMatchRace(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)

	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)

	for range 8 {
		bk := openBucket(t, v)

		wg.Go(func() {
			w := write("k", "v")

			w.IfNoneMatch = true

			_, err := bk.CreateObject(t.Context(), w)
			if err == nil {
				mu.Lock()

				wins++
				mu.Unlock()

				return
			}

			assert.ErrorIs(t, err, vfs.ErrPreconditionFailed)
		})
	}

	wg.Wait()
	assert.Equal(t, 1, wins)
}

// errStop is what Done returns in the tests.
var errStop = errors.New("stop")

// A write whose Done fails stops, returns that error, and leaves no data
// file, no index entry and no lock file; a body that fails is a BodyError,
// unless Done fails after it.
func TestCreateObject_Stopped(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	body := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(io.ErrUnexpectedEOF))

	for _, w := range []*vfs.Write{
		{Key: "k", Body: strings.NewReader("abc"), Done: func() error { return errStop }},
		{Key: "k", Body: body, Done: func() error { return nil }},
	} {
		_, err := bk.CreateObject(t.Context(), w)
		require.Error(t, err)
		assert.Empty(t, dataFiles(t, dir, "k"))
		assert.Empty(t, listKeys(t, bk, vfs.Bounds{}))
		assert.NoFileExists(t, filepath.Join(lockDir(dir), hash("k")))
	}

	_, err := bk.CreateObject(t.Context(), &vfs.Write{
		Key: "k", Body: iotest.ErrReader(io.ErrUnexpectedEOF), Done: func() error { return nil },
	})
	be, ok := errors.AsType[*vfs.BodyError](err)
	require.True(t, ok)
	require.ErrorIs(t, be, io.ErrUnexpectedEOF)

	calls := 0

	_, err = bk.CreateObject(t.Context(), &vfs.Write{
		Key:  "k",
		Body: iotest.ErrReader(io.ErrUnexpectedEOF),
		Done: func() error {
			calls++
			if calls > 1 {
				return errStop
			}

			return nil
		},
	})
	require.ErrorIs(t, err, errStop)
}

// A write commits whole: a failed commit leaves the object stored as it
// was and removes the new data file.
func TestCreateObject_CommitFails(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "kept")
	chmod(t, filepath.Join(dir, hash(testBucket)), 0o500)

	_, err := bk.CreateObject(t.Context(), write("k", "lost"))
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
	assert.Len(t, dataFiles(t, dir, "k"), 1)
	assert.Equal(t, "kept", read(t, bk, "k"))
}

// A write fails before anything is written when the objects directory or
// the keys directory is not writable, or the metadata stored cannot be
// read.
func TestCreateObject_Unwritable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	for key, prepare := range map[string]func(dir string){
		"k":     func(dir string) { chmod(t, objDir(dir), 0o500) },
		"new":   func(dir string) { chmod(t, keysDir(dir), 0o500) },
		"k\x00": func(dir string) { chmod(t, metaPath(dir, testBucket, "k"), 0o000) },
	} {
		v, dir := newVFS(t)
		bk := newBucket(t, v)

		put(t, bk, "k", "kept")
		prepare(dir)

		_, err := bk.CreateObject(t.Context(), write(strings.TrimSuffix(key, "\x00"), "lost"))
		requireNoPath(t, err, dir)
	}
}

// A long key goes under directories of the key index, which a write
// creates; they fail the write when they cannot be made.
func TestCreateObject_LongKeyIndex(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	long := strings.Repeat("a", 300)

	put(t, bk, long, "v")
	assert.Equal(t, []string{long}, listKeys(t, bk, vfs.Bounds{}))
	assert.Equal(t, "v", read(t, bk, long))

	skipIfModesIgnored(t)
	chmod(t, keysDir(dir), 0o500)

	_, err := bk.CreateObject(t.Context(), write(strings.Repeat("b", 300), "v"))
	require.ErrorIs(t, err, os.ErrPermission)
}

// writeMeta writes meta as the metadata file of key in bucket, under dir.
func writeMeta(t *testing.T, dir, bucket, key string, meta map[string]any) {
	t.Helper()

	b, err := json.Marshal(meta)
	require.NoError(t, err)
	writeFile(t, metaPath(dir, bucket, key), string(b))
}

// Metadata that does not decode, decodes to null, holds another key or
// names no valid data file, or a directory in its place, is ErrCorrupt,
// and the error quotes none of it.
func TestOpenObject_CorruptMetadata(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	for name, data := range map[string]string{
		"syntax":   `{"key": secret`,
		"type":     `{"key": 7}`,
		"null":     `null`,
		"other":    `{"key":"other","dataFile":"` + hash("other") + "_" + testUUID + `"}`,
		"datafile": `{"key":"k","dataFile":"../../secret"}`,
		"other id": `{"key":"k","dataFile":"` + hash("k") + `_not-a-uuid"}`,
		"invalid":  "{\"key\":\"k\"}\x00",
	} {
		writeFile(t, metaPath(dir, testBucket, "k"), data)

		_, err := bk.OpenObject(t.Context(), "k")
		require.ErrorIs(t, err, vfs.ErrCorrupt, name)
		requireNoPath(t, err, dir)
		assert.NotContains(t, err.Error(), "secret")
	}

	require.NoError(t, os.Remove(metaPath(dir, testBucket, "k")))
	require.NoError(t, os.Mkdir(metaPath(dir, testBucket, "k"), 0o750))

	_, err := bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)
}

// Metadata on disk is checked against every rule a write keeps: a key
// that breaks the key rules, an ETag that is not 32 lower case hex
// digits, a header field that breaks the header rules, and user metadata
// that breaks the metadata rules or has a name in upper case, are
// ErrCorrupt, and the error quotes none of it.
func TestOpenObject_MetadataRules(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	valid := func(key string) map[string]any {
		return map[string]any{
			"key": key, "etag": md5Hex("v"), "dataFile": hash(key) + "_" + testUUID,
		}
	}
	with := func(field string, value any) map[string]any {
		m := valid("k")

		m[field] = value

		return m
	}

	for name, tc := range map[string]struct {
		meta map[string]any
		key  string
	}{
		"key rules":     {valid("secret\x01"), "secret\x01"},
		"etag":          {with("etag", "secret"), "k"},
		"etag case":     {with("etag", strings.ToUpper(md5Hex("v"))), "k"},
		"header CRLF":   {with("contentType", "text/html\r\nSet-Cookie: secret"), "k"},
		"header size":   {with("cacheControl", strings.Repeat("s", 1025)), "k"},
		"expires":       {with("expires", "secret"), "k"},
		"metadata name": {with("metadata", map[string]string{"bad name": "secret"}), "k"},
		"metadata text": {with("metadata", map[string]string{"n": "s\u00e9cret"}), "k"},
		"metadata case": {with("metadata", map[string]string{"Name": "secret"}), "k"},
	} {
		writeMeta(t, dir, testBucket, tc.key, tc.meta)
		writeFile(t, filepath.Join(objDir(dir), hash(tc.key)+"_"+testUUID), "v")

		_, err := bk.OpenObject(t.Context(), tc.key)
		require.ErrorIs(t, err, vfs.ErrCorrupt, name)
		requireNoPath(t, err, dir)
		assert.NotContains(t, err.Error(), "secret", name)
	}

	writeMeta(t, dir, testBucket, "k", with("metadata", map[string]string{"name": "v"}))

	obj, err := bk.OpenObject(t.Context(), "k")
	require.NoError(t, err)
	require.NoError(t, obj.File.Close())
}

// Metadata naming a data file that is gone, or that is not a regular
// file, is ErrCorrupt from OpenObject and Stat; a missing object is
// ErrObjectNotFound.
func TestOpenObject_DataFile(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	_, err := bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrObjectNotFound)

	_, err = bk.Stat(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrObjectNotFound)

	put(t, bk, "k", "v")

	data := dataFiles(t, dir, "k")[0]
	require.NoError(t, os.Remove(data))

	_, err = bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)
	assert.Equal(t, "object cannot be read: data file is gone", err.Error())

	_, err = bk.Stat(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)

	require.NoError(t, os.Mkdir(data, 0o750))

	_, err = bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)

	_, err = bk.Stat(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)
	assert.Equal(t, filepath.Join(hash(testBucket), hash("k")+metaExt), bk.MetaPath("k"))
}

// OpenObject and Stat fail when the metadata or the data file cannot be
// read for want of permission.
func TestOpenObject_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, dataFiles(t, dir, "k")[0], 0o000)

	_, err := bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	chmod(t, objDir(dir), 0o000)

	_, err = bk.Stat(t.Context(), "k")
	require.ErrorIs(t, err, os.ErrPermission)

	chmod(t, metaPath(dir, testBucket, "k"), 0o000)

	_, err = bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, os.ErrPermission)
}

// A write over corrupt metadata replaces it without a condition, and
// meets neither IfMatch nor IfNoneMatch.
func TestCreateObject_OverCorrupt(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	writeFile(t, metaPath(dir, testBucket, "k"), "{")

	for _, w := range []*vfs.Write{{IfNoneMatch: true}, {IfMatch: md5Hex("x")}} {
		in := write("k", "v")

		in.IfMatch, in.IfNoneMatch = w.IfMatch, w.IfNoneMatch

		_, err := bk.CreateObject(t.Context(), in)
		require.ErrorIs(t, err, vfs.ErrPreconditionFailed)
	}

	put(t, bk, "k", "repaired")
	assert.Equal(t, "repaired", read(t, bk, "k"))
}

// A data file of the object replaced that cannot be removed does not
// fail the write: Written names it.
func TestCreateObject_Stale(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "old")

	old := dataFiles(t, dir, "k")[0]
	require.NoError(t, os.Remove(old))
	writeFile(t, filepath.Join(old, "x"), "")

	out := put(t, bk, "k", "new")
	assert.Equal(t, filepath.Join(objectsPath, hash(testBucket), filepath.Base(old)), out.Stale)
	require.Error(t, out.StaleErr)
	requireNoPath(t, out.StaleErr, dir)
	assert.Equal(t, "new", read(t, bk, "k"))
}

// DeleteObject removes the metadata, the data file and the index entry; a
// missing object is not an error, and corrupt metadata is ErrCorrupt and
// deletes nothing.
func TestDeleteObject(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	require.NoError(t, bk.DeleteObject(t.Context(), "k"))
	assert.NoFileExists(t, metaPath(dir, testBucket, "k"))
	assert.Empty(t, dataFiles(t, dir, "k"))
	assert.Empty(t, listKeys(t, bk, vfs.Bounds{}))
	require.NoError(t, bk.DeleteObject(t.Context(), "k"))

	writeFile(t, metaPath(dir, testBucket, "k"), "{")
	require.ErrorIs(t, bk.DeleteObject(t.Context(), "k"), vfs.ErrCorrupt)
	assert.FileExists(t, metaPath(dir, testBucket, "k"))
}

// DeleteObject fails when the metadata file cannot be removed, and keeps
// the object.
func TestDeleteObject_Unremovable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, filepath.Join(dir, hash(testBucket)), 0o500)

	err := bk.DeleteObject(t.Context(), "k")
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
	assert.Equal(t, "v", read(t, bk, "k"))
}

// DeleteObject reports a data file it cannot remove, though the object is
// deleted.
func TestDeleteObject_DataLeft(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")

	data := dataFiles(t, dir, "k")[0]
	require.NoError(t, os.Remove(data))
	writeFile(t, filepath.Join(data, "x"), "")

	err := bk.DeleteObject(t.Context(), "k")
	requireNoPath(t, err, dir)

	_, err = bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrObjectNotFound)
}

// The operations of a closed Bucket, and those whose context has ended,
// fail.
func TestBucket_Closed(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	bk, err := v.OpenBucket(testBucket)
	require.NoError(t, err)
	require.NoError(t, bk.Close())

	_, err = bk.CreateObject(t.Context(), write("k", "v"))
	require.ErrorIs(t, err, os.ErrClosed)

	_, err = bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, os.ErrClosed)
	require.ErrorIs(t, bk.DeleteObject(t.Context(), "k"), os.ErrClosed)

	open := openBucket(t, v)
	ctx, cancel := ctxCanceled(t)
	cancel()

	hold := holdKey(t, dir, "k")
	defer hold()

	_, err = open.CreateObject(ctx, write("k", "v"))
	require.ErrorIs(t, err, ctx.Err())
}

// Writes and reads of one key from many Buckets see whole objects only,
// and leave one data file.
func TestBucket_ConcurrentSameKey(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	var wg sync.WaitGroup

	for i := range 6 {
		bk := openBucket(t, v)

		wg.Go(func() {
			body := strings.Repeat(strconv.Itoa(i), 1000)

			for range 20 {
				_, err := bk.CreateObject(t.Context(), write("k", body))
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}

				obj, err := bk.OpenObject(t.Context(), "k")
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}

				b, err := io.ReadAll(obj.File)
				assert.NoError(t, err)
				assert.NoError(t, obj.File.Close())
				assert.Equal(t, md5Hex(string(b)), obj.ETag)
			}
		})
	}

	wg.Wait()
	assert.Len(t, dataFiles(t, dir, "k"), 1)
}

// The object operations whose key another holder has return the context's
// error when it ends first.
func TestBucket_LockWaitCanceled(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	require.NoError(t, os.Remove(dataFiles(t, dir, "k")[0]))

	release := holdKey(t, dir, "k")
	defer release()

	ctx, cancel := ctxCanceled(t)
	cancel()

	_, err := bk.OpenObject(ctx, "k")
	require.ErrorIs(t, err, ctx.Err())
	require.ErrorIs(t, bk.DeleteObject(ctx, "k"), ctx.Err())

	_, err = bk.Stat(ctx, "k")
	require.ErrorIs(t, err, ctx.Err())

	_, err = bk.CreateObject(ctx, write("k", "v"))
	require.ErrorIs(t, err, ctx.Err())
}

// A write over metadata that holds another key replaces it; one whose
// metadata file is a directory that is not empty fails at the commit, and
// leaves no new data file.
func TestCreateObject_CommitOverDirectory(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	writeMeta(t, dir, testBucket, "k", map[string]any{
		"key": "other", "dataFile": hash("k") + "_" + testUUID,
	})

	_, err := bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)
	assert.Equal(t, "object cannot be read: meta file holds another key", err.Error())

	require.NoError(t, os.Remove(metaPath(dir, testBucket, "k")))
	writeFile(t, filepath.Join(metaPath(dir, testBucket, "k"), "x"), "")

	_, err = bk.CreateObject(t.Context(), write("k", "v"))
	require.Error(t, err)
	requireNoPath(t, err, dir)
	assert.Empty(t, dataFiles(t, dir, "k"))
}

// HeadBucket fails when the root cannot be read.
func TestHeadBucket_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	chmod(t, dir, 0o000)

	err := v.HeadBucket(testBucket)
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
}
