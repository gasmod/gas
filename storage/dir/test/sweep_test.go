package dir_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// uuid is a UUID that names the temp files and the data files the tests
// plant.
const uuid = "00000000-0000-4000-8000-000000000000"

// leftovers plants what crashed writes leave in the directory path, whose
// bucket holds the objects `k1` and `k2`, and returns their paths,
// relative to the directory.
func leftovers(t *testing.T, path string) []string {
	t.Helper()

	b := sha(bucket)
	rel := []string{
		filepath.Join(b, sha("k1")+".gas.json"+uuid),
		filepath.Join(".gas", "objects", b, sha("k2")+"_"+uuid),
		filepath.Join(".gas", "objects", b, sha("k3")+"_"+uuid),
		filepath.Join(".gas", "keys", b, hex.EncodeToString([]byte("ghost"))+".k"),
		filepath.Join(".gas", "buckets", b+uuid),
	}

	for _, r := range rel {
		require.NoError(t, os.WriteFile(filepath.Join(path, r), []byte("x"), 0o600))
	}

	return rel
}

// Init removes what crashed writes left: temp files of metadata and of
// name records, data files no metadata names, and index entries of keys
// with no object; it passes each to the skip handler, logs how many, and
// keeps the objects.
func TestInit_SweepsLeftovers(t *testing.T) {
	t.Parallel()

	path := t.TempDir()
	p, _ := build(t, dir.WithConfig(config(path, bucket)))
	ctx := t.Context()

	require.NoError(t, p.Init(ctx))
	put(t, p, "k1", "one")
	put(t, p, "k2", "two")
	require.NoError(t, p.Close(ctx))

	planted := leftovers(t, path)

	var got skips

	p, log := build(t, dir.WithConfig(config(path, bucket)), dir.WithSkipHandler(got.handler()))
	require.NoError(t, p.Init(ctx))

	defer func() { require.NoError(t, p.Close(ctx)) }()

	all := got.all()
	paths := make([]string, 0, len(all))

	for _, s := range all {
		paths = append(paths, s.path)

		require.ErrorContains(t, s.err, "removed: ")
		requireClean(t, s.err, path, bucket)
	}

	assert.ElementsMatch(t, planted, paths)

	for _, r := range planted {
		assert.NoFileExists(t, filepath.Join(path, r))
	}

	e, ok := log.find("leftovers of crashed writes removed")
	require.True(t, ok)
	assert.Equal(t, len(planted), e.fields["removed"])
	assert.Equal(t, "one", read(t, p, "k1"))
	assert.Equal(t, "two", read(t, p, "k2"))
}

// Init looks at no more than `storage.sweeplimit` entries of the
// directory, then stops and logs that it did.
func TestInit_SweepLimit(t *testing.T) {
	t.Parallel()

	const limit = 10

	path := t.TempDir()
	p, _ := build(t, dir.WithConfig(config(path, bucket)))
	ctx := t.Context()

	require.NoError(t, p.Init(ctx))
	require.NoError(t, p.Close(ctx))

	buckets := filepath.Join(path, ".gas", "buckets")
	for i := range limit + 1 {
		require.NoError(t, os.WriteFile(filepath.Join(buckets, "x"+strconv.Itoa(i)), nil, 0o600))
	}

	// Last in byte order: the sweep stops before it gets to it.
	temp := filepath.Join(path, sha(bucket), sha("k")+".gas.json"+uuid)
	require.NoError(t, os.WriteFile(temp, nil, 0o600))

	cfg := config(path, bucket)

	cfg.Storage.SweepLimit = limit

	p, log := build(t, dir.WithConfig(cfg))
	require.NoError(t, p.Init(ctx))
	require.NoError(t, p.Close(ctx))

	e, ok := log.find("leftover sweep stopped at its limit")
	require.True(t, ok)
	assert.Equal(t, "warn", e.level)
	assert.Equal(t, limit, e.fields["limit"])
	assert.FileExists(t, temp)
}

// The leftovers of a crash are seen by no read or listing before Init
// removes them.
func TestLeftovers_Unseen(t *testing.T) {
	t.Parallel()

	p, path := open(t)

	put(t, p, "k1", "one")
	put(t, p, "k2", "two")
	leftovers(t, path)

	keys := listKeys(t, p, &storage.ListObjectsInput{})
	assert.Equal(t, []string{"k1", "k2"}, keys)
	assert.Equal(t, "one", read(t, p, "k1"))
	assert.Equal(t, "two", read(t, p, "k2"))
	assert.False(
		t,
		slices.ContainsFunc(keys, func(k string) bool { return strings.Contains(k, "ghost") }),
	)
}
