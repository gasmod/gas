package filename_test

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/migrate/internal/filename"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	name, ok := filename.Parse("sql/20250216001_create_users.up.sql")

	require.True(t, ok)
	assert.Equal(t, filename.Name{
		Stem:        "20250216001_create_users",
		Version:     "20250216001",
		Description: "create users",
		Kind:        filename.Up,
	}, name)
}

// Parse reads the base name: the suffix gives the kind, the stem up to
// its first underscore the version, and the rest the description.
func TestParse(t *testing.T) {
	t.Parallel()

	cases := map[string]filename.Name{
		"001.up.sql": {Stem: "001", Version: "001", Kind: filename.Up},
		"a/b/001_x_y.down.sql": {
			Stem:        "001_x_y",
			Version:     "001",
			Description: "x y",
			Kind:        filename.Down,
		},
		"_x.up.sql":     {Stem: "_x", Description: "x", Kind: filename.Up},
		".up.sql":       {Kind: filename.Up},
		"001_.down.sql": {Stem: "001_", Version: "001", Kind: filename.Down},
		"v1.2_add__index.up.sql": {
			Stem:        "v1.2_add__index",
			Version:     "v1.2",
			Description: "add  index",
			Kind:        filename.Up,
		},
	}

	for in, want := range cases {
		got, ok := filename.Parse(in)
		require.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}

	for _, in := range []string{"001.sql", "001.up.SQL", "README.md", "", "up.sql"} {
		_, ok := filename.Parse(in)
		assert.False(t, ok, in)
	}
}

// FuzzParse checks, for any name, that Parse does not panic, gives the
// same answer twice, accepts exactly the names whose base name has one
// of the two suffixes, and that the stem, version and description fit
// together: the version has no underscore, and the stem is the version
// alone or the version, an underscore and the description with its
// spaces read as underscores where the stem had them.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"001_create_users.up.sql", "dir/002.down.sql", "_.up.sql", ".down.sql",
		"a_b c.up.sql", "x.sql", "", "/", "a/.up.sql/b",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		got, ok := filename.Parse(in)

		again, okAgain := filename.Parse(in)
		require.Equal(t, ok, okAgain)
		require.Equal(t, got, again)

		base := path.Base(in)
		up, down := strings.HasSuffix(base, ".up.sql"), strings.HasSuffix(base, ".down.sql")
		require.Equal(t, up || down, ok)

		if !ok {
			require.Equal(t, filename.Name{}, got)

			return
		}

		require.Equal(t, up, got.Kind == filename.Up)
		require.NotContains(t, got.Version, "_")
		require.True(t, strings.HasPrefix(base, got.Stem))

		if got.Stem == got.Version {
			require.Empty(t, got.Description)

			return
		}

		rest := strings.TrimPrefix(got.Stem, got.Version+"_")
		require.Equal(t, got.Version+"_"+rest, got.Stem)
		require.Equal(t, strings.ReplaceAll(rest, "_", " "), got.Description)
	})
}
