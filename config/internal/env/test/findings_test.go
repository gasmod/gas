package env_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/env"
)

// KI-059: a name's nesting depth must cost memory in proportion to its
// length. insert used to join the levels seen so far into a new path string
// at every level and keep each one in owners, so a name of n levels
// allocated and held about n² bytes: 27 MiB for this 14 KiB name, and
// 1.6 GiB for a 117 KiB one, under the 128 KiB Linux allows one environment
// string. A `.env` key has no length limit. 1 KiB per level is twice what a
// linear implementation allocates.
//
// Not parallel: it reads process-wide allocation counters, and a test that
// is not parallel never runs alongside one that is.
func TestParseVariables_DeepNameAllocatesLinearly(t *testing.T) {
	const levels = 5_000

	name := strings.Repeat("A__", levels-1) + "A"

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	got, err := env.ParseVariables(map[string]string{name: "1"}, "", "__")

	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.NotEmpty(t, got)

	allocated := after.TotalAlloc - before.TotalAlloc
	assert.LessOrEqual(
		t,
		allocated,
		uint64(levels<<10),
		"ParseVariables allocated %d MiB for a name of %d levels",
		allocated>>20,
		levels,
	)
}

// KI-061: DYLD_VERSIONED_LIBRARY_PATH and DYLD_VERSIONED_FRAMEWORK_PATH name
// directories of override libraries that dyld loads in place of the
// installed ones when their version is higher (dyld(1)), the way
// DYLD_LIBRARY_PATH does, so they belong with the dynamic linker's
// code-execution vectors.
func TestIsUnsafeVar_DyldVersionedPaths(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"DYLD_VERSIONED_LIBRARY_PATH", "DYLD_VERSIONED_FRAMEWORK_PATH"} {
		assert.True(t, env.IsUnsafeVar(name), name)
	}
}

// bash reads and runs the file named in BASH_ENV before it runs a script
// (bash(1)), so a `.env` entry copied into the process environment would run
// that file in every bash script the process starts.
func TestIsUnsafeVar_BashEnv(t *testing.T) {
	t.Parallel()

	assert.True(t, env.IsUnsafeVar("BASH_ENV"))
}
