package config_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

func TestProviders_Names(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Environment Variables", config.EnvProviderName)
	assert.Equal(t, config.EnvProviderName, config.NewEnvProvider().Name())
	assert.Equal(t, "DotEnv", config.NewDotEnvProvider().Name())
	assert.Equal(t, "JSON", config.NewJSONProvider().Name())
}

// The env provider splits names on `__` into levels and lowercases them.
// A prefix keeps only the names that start with it, without regard to
// case, and is removed; the separator is not added to it. Unsafe variables
// are left out.
//
// Not parallel: it sets environment variables.
func TestEnvProvider(t *testing.T) {
	t.Setenv("CFGTEST_DB__HOST", "db")
	t.Setenv("CFGTEST_DB__PORT", "5432")
	t.Setenv("CFGTEST_DEBUG", "true")
	t.Setenv("OTHER_CFGTEST", "x")

	got, err := config.NewEnvProvider(config.WithEnvPrefix("cfgtest_")).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"db": m{"host": "db", "port": "5432"}, "debug": "true"}, got)

	got, err = config.NewEnvProvider(config.WithEnvPrefix("CFGTEST")).Load()
	require.NoError(t, err)
	assert.Equal(t, "true", got["_debug"])

	got, err = config.NewEnvProvider(
		config.WithEnvPrefix("CFGTEST_"),
		config.WithEnvSeparator(""),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"db__host": "db", "db__port": "5432", "debug": "true"}, got)

	got, err = config.NewEnvProvider(config.WithoutEnvPrefix()).Load()
	require.NoError(t, err)
	assert.Equal(t, "true", got["cfgtest_debug"])
	assert.NotContains(t, got, "path")
	assert.NotContains(t, got, "home")
}

// A provider with no prefix reads nothing unless WithoutEnvPrefix opts in;
// an empty prefix is no prefix, and the last of the two options wins.
//
// Not parallel: it sets an environment variable.
func TestEnvProvider_PrefixRequired(t *testing.T) {
	t.Setenv("CFGTEST_DEBUG", "true")

	for name, opts := range map[string][]config.EnvProviderOption{
		"none":           nil,
		"empty prefix":   {config.WithEnvPrefix("")},
		"prefix wins":    {config.WithoutEnvPrefix(), config.WithEnvPrefix("")},
		"separator only": {config.WithEnvSeparator("_")},
	} {
		got, err := config.NewEnvProvider(opts...).Load()
		require.ErrorIs(t, err, config.ErrEnvPrefixRequired, name)
		require.EqualError(t, err, "environment prefix is required", name)
		assert.Nil(t, got, name)
	}

	got, err := config.NewEnvProvider(config.WithEnvPrefix("CFGTEST_"), config.WithoutEnvPrefix()).
		Load()
	require.NoError(t, err)
	assert.Equal(t, "true", got["cfgtest_debug"])
}

// Two variables that map to the same key, or where one needs the other's
// key as a map, are an error.
//
// Not parallel: it sets environment variables.
func TestEnvProvider_Conflict(t *testing.T) {
	t.Setenv("CFGTEST_A", "1")
	t.Setenv("CFGTEST_A__B", "2")

	_, err := config.NewEnvProvider(config.WithEnvPrefix("CFGTEST_")).Load()
	require.EqualError(t, err, `conflicting variable names: "CFGTEST_A" and "CFGTEST_A__B"`)
}

func dotenvFS(content string) fstest.MapFS {
	return fstest.MapFS{".env": {Data: []byte(content)}}
}

// The `.env` provider reads `.env` and turns its names into keys as the env
// provider does, without a prefix, and leaves the unsafe ones out.
func TestDotEnvProvider(t *testing.T) {
	t.Parallel()

	got, err := config.NewDotEnvProvider(config.WithDotEnvFileFS(dotenvFS(`
# database
DB__HOST=db
DB__PORT = 5432
DB__URL="postgres://${DB__HOST}:${DB__PORT}"
PATH=/evil
`))).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"db": m{
		"host": "db",
		"port": "5432",
		"url":  "postgres://db:5432",
	}}, got)

	got, err = config.NewDotEnvProvider(
		config.WithDotEnvFileFS(fstest.MapFS{"conf/app.env": {Data: []byte("A_B=1\n")}}),
		config.WithDotEnvFilePath("conf/app.env"),
		config.WithDotEnvSeparator("_"),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"a": m{"b": "1"}}, got)
}

// A missing file is an error unless the provider is told it is optional.
func TestDotEnvProvider_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := config.NewDotEnvProvider(config.WithDotEnvFileFS(fstest.MapFS{})).Load()
	require.ErrorIs(t, err, config.ErrDotEnvFileReadFailed)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.EqualError(t, err, "failed to read .env file .env: open .env: file does not exist")

	got, err := config.NewDotEnvProvider(
		config.WithDotEnvFileFS(fstest.MapFS{}),
		config.WithDotEnvFileNotFoundPanic(false),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{}, got)
}

func TestDotEnvProvider_Errors(t *testing.T) {
	t.Parallel()

	_, err := config.NewDotEnvProvider(config.WithDotEnvFilePath("")).Load()
	require.ErrorIs(t, err, config.ErrDotEnvFilePathNotSet)
	require.EqualError(t, err, ".env file path is not set")

	_, err = config.NewDotEnvProvider(config.WithDotEnvFileFS(dotenvFS("A=x y\n"))).Load()
	require.ErrorIs(t, err, config.ErrDotEnvParseFailed)
	require.EqualError(
		t,
		err,
		`failed to parse .env file .env: 1:5: unexpected character, want newline: key: "A"`,
	)

	_, err = config.NewDotEnvProvider(config.WithDotEnvFileFS(dotenvFS("A=1\nA__B=2\n"))).Load()
	require.ErrorIs(t, err, config.ErrDotEnvParseFailed)
	require.EqualError(
		t,
		err,
		`failed to parse .env file .env: conflicting variable names: "A" and "A__B"`,
	)
}

// With WithDotEnvFileAppendToOSEnv, each load also copies the variables
// into the process environment, apart from the unsafe ones.
//
// Not parallel: it changes the process environment.
func TestDotEnvProvider_AppendToOSEnv(t *testing.T) {
	t.Setenv("CFGTEST_APPEND", "old")
	t.Setenv("PATH", os.Getenv("PATH"))

	path := os.Getenv("PATH")

	content := "CFGTEST_APPEND=new\nCFGTEST_ADDED=x\nPATH=/evil\n"
	_, err := config.NewDotEnvProvider(config.WithDotEnvFileFS(dotenvFS(content))).Load()
	require.NoError(t, err)
	assert.Equal(t, "old", os.Getenv("CFGTEST_APPEND"), "copied without the option")

	t.Cleanup(func() { _ = os.Unsetenv("CFGTEST_ADDED") })

	_, err = config.NewDotEnvProvider(
		config.WithDotEnvFileFS(dotenvFS(content)),
		config.WithDotEnvFileAppendToOSEnv(true),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, "new", os.Getenv("CFGTEST_APPEND"))
	assert.Equal(t, "x", os.Getenv("CFGTEST_ADDED"))
	assert.Equal(t, path, os.Getenv("PATH"))
}

// A file that fails to parse leaves the process environment as it was.
//
// Not parallel: it changes the process environment.
func TestDotEnvProvider_AppendToOSEnvOnlyOnSuccess(t *testing.T) {
	t.Setenv("CFGTEST_APPEND", "old")

	_, err := config.NewDotEnvProvider(
		config.WithDotEnvFileFS(dotenvFS("CFGTEST_APPEND=new\nCFGTEST_APPEND__X=1\n")),
		config.WithDotEnvFileAppendToOSEnv(true),
	).Load()
	require.ErrorIs(t, err, config.ErrDotEnvParseFailed)
	assert.Equal(t, "old", os.Getenv("CFGTEST_APPEND"))
}

// When a variable cannot be copied into the process environment, such as
// one whose value holds a NUL, the ones already copied are restored: those
// the process had get their old value back, and the others are unset.
//
// Not parallel: it changes the process environment.
func TestDotEnvProvider_AppendToOSEnvRollsBack(t *testing.T) {
	var file strings.Builder

	// Map order is random; with many variables, some are almost always
	// copied before the bad one, so the restore runs.
	for i := range 20 {
		name := fmt.Sprintf("CFGTEST_RB_%02d", i)
		t.Setenv(name, "old")
		require.NoError(t, os.Unsetenv(name))

		if i%2 == 0 {
			t.Setenv(name, "old")
		}

		fmt.Fprintf(&file, "%s=new\n", name)
	}

	file.WriteString("CFGTEST_RB_BAD=a\x00b\n")
	t.Setenv("CFGTEST_RB_BAD", "")
	require.NoError(t, os.Unsetenv("CFGTEST_RB_BAD"))

	_, err := config.NewDotEnvProvider(
		config.WithDotEnvFileFS(dotenvFS(file.String())),
		config.WithDotEnvFileAppendToOSEnv(true),
	).Load()
	require.ErrorIs(t, err, config.ErrSetEnv)
	assert.NotContains(t, err.Error(), "a\x00b")

	for i := range 20 {
		name := fmt.Sprintf("CFGTEST_RB_%02d", i)
		value, ok := os.LookupEnv(name)

		if i%2 == 0 {
			assert.Equal(t, "old", value, name)
		} else {
			assert.False(t, ok, name)
		}
	}
}

// The JSON provider reads one object. An integer is an int64.
func TestJSONProvider(t *testing.T) {
	t.Parallel()

	got, err := config.NewJSONProvider(
		config.WithJSONFilePath("conf/app.json"),
		config.WithJSONFileFS(fstest.MapFS{
			"conf/app.json": {Data: []byte(`{"DB": {"Host": "db", "port": 5432}, "tags": ["a"]}`)},
		}),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"DB": m{"Host": "db", "port": int64(5432)}, "tags": []any{"a"}}, got)
}

// A JSON integer reaches a field with every digit, and a string field
// takes it without an exponent.
func TestJSONProvider_NumbersBind(t *testing.T) {
	t.Parallel()

	cfg := config.New(config.WithProvider(config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(fstest.MapFS{
			"app.json": {
				Data: []byte(`{"id": 1234567890123456789, "account": 1234567, "ratio": 0.5}`),
			},
		}),
	)))
	require.NoError(t, cfg.Load())

	var got struct {
		Account string
		ID      int64
		Ratio   float64
	}

	require.NoError(t, cfg.Bind(&got))
	assert.Equal(t, int64(1234567890123456789), got.ID)
	assert.Equal(t, "1234567", got.Account)
	assert.Equal(t, 0.5, got.Ratio) //nolint:testifylint // 0.5 is exact
}

func TestJSONProvider_Errors(t *testing.T) {
	t.Parallel()

	_, err := config.NewJSONProvider().Load()
	require.ErrorIs(t, err, config.ErrJSONFilePathNotSet)
	require.EqualError(t, err, "JSON file path is not set")

	_, err = config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(fstest.MapFS{}),
	).Load()
	require.ErrorIs(t, err, config.ErrJSONFileReadFailed)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.EqualError(
		t,
		err,
		"failed to read JSON config file app.json: open app.json: file does not exist",
	)

	_, err = config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(fstest.MapFS{"app.json": {Data: []byte(`["a"]`)}}),
	).Load()
	require.ErrorIs(t, err, config.ErrJSONDecodeFailed)
	require.EqualError(
		t,
		err,
		"failed to decode JSON from app.json: value is a JSON array, not an object",
	)
}

// By default the file providers read from the working directory, and
// refuse a file outside it, one that is not a regular file, or one larger
// than 1 MiB. The error names the file once.
//
// Not parallel: it changes the working directory.
func TestFileProviders_DefaultFS(t *testing.T) {
	dir := chdir(t)
	require.NoError(t, os.Mkdir("conf", 0o700))
	writeFile(t, "conf/app.json", `{"name": "json"}`)
	writeFile(t, ".env", "NAME=dotenv\n")
	writeFile(t, "big.json", string(make([]byte, 1<<20+1)))
	writeFile(t, filepath.Join(filepath.Dir(dir), "outside.json"), `{}`)

	for _, path := range []string{"conf/app.json", "./conf/app.json", filepath.Join(dir, "conf/app.json")} {
		got, err := config.NewJSONProvider(config.WithJSONFilePath(path)).Load()
		require.NoError(t, err, path)
		assert.Equal(t, m{"name": "json"}, got, path)
	}

	got, err := config.NewDotEnvProvider().Load()
	require.NoError(t, err)
	assert.Equal(t, m{"name": "dotenv"}, got)

	for path, want := range map[string]string{
		"big.json":         "failed to read JSON config file big.json: config file too large",
		"conf":             "failed to read JSON config file conf: not a regular file",
		"../outside.json":  "failed to read JSON config file ../outside.json: unsafe file path: outside allowed directory",
		"conf/../app.json": "failed to read JSON config file conf/../app.json: openat app.json: no such file or directory",
	} {
		_, err := config.NewJSONProvider(config.WithJSONFilePath(path)).Load()
		require.ErrorIs(t, err, config.ErrJSONFileReadFailed, path)
		require.EqualError(t, err, want, path)
	}
}

// A nil file system means the default one.
//
// Not parallel: it changes the working directory.
func TestFileProviders_NilFS(t *testing.T) {
	chdir(t)
	writeFile(t, "app.json", `{"name": "json"}`)
	writeFile(t, ".env", "NAME=dotenv\n")

	got, err := config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(nil),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"name": "json"}, got)

	got, err = config.NewDotEnvProvider(config.WithDotEnvFileFS(nil)).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"name": "dotenv"}, got)
}
