package dir_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// The environment variables that make a run of this test binary a child
// of a cross-process test: the directory, the child's name, and the test
// that started it.
const (
	childDirEnv  = "GAS_STORAGE_DIR_CHILD_DIR"
	childIDEnv   = "GAS_STORAGE_DIR_CHILD_ID"
	childTestEnv = "GAS_STORAGE_DIR_CHILD_TEST"
)

// The sizes of the cross-process tests.
const (
	// children is the number of processes that share the directory.
	children = 4
	// writerRounds is the number of PutObject and GetObject pairs per
	// writer.
	writerRounds = 100
	// writerBodySize is the size of every body a writer writes.
	writerBodySize = 64 << 10
	// cyclerRounds is the number of create, write, delete cycles per
	// cycler.
	cyclerRounds = 100
	// cycled is the bucket every cycler creates and deletes.
	cycled = "cycled-bucket"
)

// runChildren runs this test binary children times as the child test,
// each on the directory path, and fails t unless each passes.
func runChildren(t *testing.T, test, path string) {
	t.Helper()

	cmds := make([]*exec.Cmd, 0, children)
	outs := make([]*bytes.Buffer, 0, children)

	for i := range children {
		id := string(rune('A' + i))
		//nolint:gosec // G204: this test binary, with fixed arguments.
		cmd := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run", "^"+test+"$", "-test.count=1", "-test.v")

		cmd.Env = append(
			os.Environ(),
			childDirEnv+"="+path,
			childIDEnv+"="+id,
			childTestEnv+"="+test,
		)

		out := &bytes.Buffer{}

		cmd.Stdout, cmd.Stderr = out, out

		require.NoError(t, cmd.Start())

		cmds = append(cmds, cmd)
		outs = append(outs, out)
	}

	for i, cmd := range cmds {
		err := cmd.Wait()
		require.NoError(t, err, outs[i].String())
		assert.Contains(t, outs[i].String(), "--- PASS: "+test, outs[i].String())
	}
}

// child returns the directory and the name of this run when it is a child
// started for test, and skips t otherwise.
func child(t *testing.T, test string) (string, string) {
	t.Helper()

	if os.Getenv(childTestEnv) != test {
		t.Skip("runs only as a child of a cross-process test")
	}

	return os.Getenv(childDirEnv), os.Getenv(childIDEnv)
}

// parent skips t when this run is a child of a cross-process test.
func parent(t *testing.T) {
	t.Helper()

	if os.Getenv(childTestEnv) != "" {
		t.Skip("running as a child of a cross-process test")
	}
}

// countUnnamed returns a skip handler that counts the buckets that cannot
// be named into n.
func countUnnamed(n *atomic.Int64) dir.SkipHandler {
	return func(_ string, err error) {
		if errors.Is(err, dir.ErrUnnamedBucket) {
			n.Add(1)
		}
	}
}

// The lock of a key excludes other processes that share the directory:
// processes that each write and read one key through their own store
// always read a whole object, a body, a content type and metadata of one
// write, and one data file is left.
//
// Not parallel: it runs processes that use the CPU it would share.
func TestCrossProcess_Locking(t *testing.T) {
	parent(t)

	p, path := open(t)

	runChildren(t, "TestCrossProcess_Writer", path)
	assert.Len(t, dataFiles(t, path, "shared"), 1)
	assert.NotEmpty(t, read(t, p, "shared"))
}

// The child half of TestCrossProcess_Locking: it writes and reads one key,
// with a body, a content type and metadata that each spell one letter,
// and fails on a read whose three disagree.
func TestCrossProcess_Writer(t *testing.T) {
	t.Parallel()

	path, id := child(t, "TestCrossProcess_Writer")
	p := openAt(t, path)
	ctx := context.Background()

	for i := range writerRounds {
		letter := id
		if i%2 == 1 {
			letter = strings.ToLower(id)
		}

		in := putIn("shared", strings.Repeat(letter, writerBodySize))

		in.ContentType = new("x/" + letter)
		in.Metadata = map[string]string{"letter": letter}

		_, err := p.PutObject(ctx, in)
		require.NoError(t, err, i)

		obj, err := p.GetObject(ctx, getIn("shared"))
		require.NoError(t, err, i)

		body, err := io.ReadAll(obj.Body)
		require.NoError(t, err)
		require.NoError(t, obj.Body.Close())

		want := strings.TrimPrefix(obj.ContentType, "x/")
		require.Equal(t, want, obj.Metadata["letter"], i)
		require.Equal(t, strings.Repeat(want, writerBodySize), string(body), i)
	}
}

// CreateBucket and DeleteBucket of one name never interleave across
// processes: processes that each create a bucket, write and delete an
// object in it, and delete it, over and over, see the bucket missing or
// not empty at times, and nothing else, a write racing a delete included,
// and leave it with both its name record and its metadata directory or
// neither, and no lifecycle lock.
//
// Not parallel: it runs processes that use the CPU it would share.
func TestCrossProcess_BucketLifecycle(t *testing.T) {
	parent(t)

	var unnamed atomic.Int64

	path := t.TempDir()
	p, _ := build(t, dir.WithConfig(config(path, "")), dir.WithSkipHandler(countUnnamed(&unnamed)))
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(context.Background())) }()

	runChildren(t, "TestCrossProcess_BucketCycler", path)

	_, recordErr := os.Lstat(nameRecord(path, cycled))
	_, metaErr := os.Lstat(filepath.Join(path, sha(cycled)))
	assert.Equal(
		t,
		recordErr == nil,
		metaErr == nil,
		"name record: %v, metadata dir: %v",
		recordErr,
		metaErr,
	)

	_, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
	require.NoError(t, err)
	assert.Zero(t, unnamed.Load())

	locks, err := filepath.Glob(filepath.Join(path, ".gas", "locks", "*.bucket"))
	require.NoError(t, err)
	assert.Empty(t, locks)
}

// The child half of TestCrossProcess_BucketLifecycle: each cycle creates
// the shared bucket, writes and deletes its own object in it, deletes the
// bucket and lists the buckets, which never holds one that cannot be
// named.
func TestCrossProcess_BucketCycler(t *testing.T) {
	t.Parallel()

	path, id := child(t, "TestCrossProcess_BucketCycler")

	var unnamed atomic.Int64

	p, err := dir.New(
		dir.WithConfig(config(path, "")),
		dir.WithSkipHandler(countUnnamed(&unnamed)),
	)(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, p.Init(t.Context()))

	defer func() { require.NoError(t, p.Close(context.Background())) }()

	ctx := context.Background()
	obj := storage.BaseObjectInput{Bucket: new(cycled), Key: "from-" + id}

	for i := range cyclerRounds {
		require.NoError(t, p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: cycled}), i)

		_, err = p.PutObject(
			ctx,
			&storage.PutObjectInput{BaseObjectInput: obj, Body: strings.NewReader(id)},
		)
		if err != nil {
			require.ErrorIs(t, err, storage.ErrBucketNotFound, i)
		}

		err = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: obj})
		if err != nil {
			require.ErrorIs(t, err, storage.ErrBucketNotFound, i)
		}

		err = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: cycled})
		if err != nil && !errors.Is(err, storage.ErrBucketNotEmpty) {
			require.ErrorIs(t, err, storage.ErrBucketNotFound, i)
		}

		_, err = p.ListBuckets(ctx, &storage.ListBucketsInput{})
		require.NoError(t, err, i)
		require.Zero(t, unnamed.Load(), i)
	}
}
