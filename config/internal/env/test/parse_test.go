package env_test

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/internal/env"
)

type m = map[string]any

// parseCase is an input that must parse, and the map it must parse to.
type parseCase struct {
	vars   map[string]string
	want   m
	name   string
	prefix string
	sep    string
}

func runParseCases(t *testing.T, tests []parseCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := env.ParseVariables(tt.vars, tt.prefix, tt.sep)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseVariables_Nesting(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{name: "empty input", vars: map[string]string{}, sep: "__", want: m{}},
		{name: "nil input", vars: nil, sep: "__", want: m{}},
		{name: "flat name", vars: map[string]string{"A": "1"}, sep: "__", want: m{"a": "1"}},
		{
			name: "two levels",
			vars: map[string]string{"MY__ENV": "val"},
			sep:  "__",
			want: m{"my": m{"env": "val"}},
		},
		{
			name: "three levels",
			vars: map[string]string{"DB__HOST__PORT": "5432"},
			sep:  "__",
			want: m{"db": m{"host": m{"port": "5432"}}},
		},
		{
			name: "siblings share a map",
			vars: map[string]string{"DB__HOST": "h", "DB__PORT": "p"},
			sep:  "__",
			want: m{"db": m{"host": "h", "port": "p"}},
		},
		{
			name: "single underscore is not the separator",
			vars: map[string]string{"DB_HOST": "h"},
			sep:  "__",
			want: m{"db_host": "h"},
		},
		{
			name: "empty separator keeps names flat",
			vars: map[string]string{"DB__HOST": "h"},
			want: m{"db__host": "h"},
		},
		{
			name: "values kept exactly",
			vars: map[string]string{"A": " x \n", "B": ""},
			sep:  "__",
			want: m{"a": " x \n", "b": ""},
		},
	})
}

func TestParseVariables_Prefix(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name:   "prefix stripped before nesting",
			vars:   map[string]string{"APP_DB__HOST": "localhost"},
			prefix: "APP_",
			sep:    "__",
			want:   m{"db": m{"host": "localhost"}},
		},
		{
			name:   "names without prefix left out",
			vars:   map[string]string{"APP_A": "1", "OTHER": "2", "XAPP_B": "3"},
			prefix: "APP_",
			sep:    "__",
			want:   m{"a": "1"},
		},
		{
			name:   "prefix matched without regard to case",
			vars:   map[string]string{"app_A": "1"},
			prefix: "APP_",
			sep:    "__",
			want:   m{"a": "1"},
		},
		{
			name:   "prefix not trimmed",
			vars:   map[string]string{"APP_A": "1"},
			prefix: " APP_",
			sep:    "__",
			want:   m{},
		},
		{
			name:   "prefix and separator overlap",
			vars:   map[string]string{"APP__A__B": "1"},
			prefix: "APP__",
			sep:    "__",
			want:   m{"a": m{"b": "1"}},
		},
	})
}

func TestParseVariables_Case(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "names lowercased",
			vars: map[string]string{"MiXeD": "1"},
			sep:  "__",
			want: m{"mixed": "1"},
		},
		{
			name: "non-ASCII lowercased",
			vars: map[string]string{"ÉTÉ": "1"},
			sep:  "__",
			want: m{"été": "1"},
		},
		{
			name: "names not trimmed",
			vars: map[string]string{" A ": "1"},
			sep:  "__",
			want: m{" a ": "1"},
		},
		// KI-055: a separator with uppercase letters matched nothing.
		{
			name: "uppercase separator",
			vars: map[string]string{"A_X_B": "1"},
			sep:  "_X_",
			want: m{"a": m{"b": "1"}},
		},
		{
			name: "lowercase separator matches uppercase name",
			vars: map[string]string{"A_X_B": "1"},
			sep:  "_x_",
			want: m{"a": m{"b": "1"}},
		},
	})
}

// A name with an empty level is left out, the rest of the input is kept.
func TestParseVariables_EmptyLevelsAreLeftOut(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "leading separator",
			vars: map[string]string{"__A": "1", "B": "2"},
			sep:  "__",
			want: m{"b": "2"},
		},
		{
			name: "trailing separator",
			vars: map[string]string{"A__": "1", "B": "2"},
			sep:  "__",
			want: m{"b": "2"},
		},
		{
			name: "doubled separator",
			vars: map[string]string{"A____B": "1", "B": "2"},
			sep:  "__",
			want: m{"b": "2"},
		},
		{name: "only separator", vars: map[string]string{"__": "1"}, sep: "__", want: m{}},
		{name: "empty name", vars: map[string]string{"": "1"}, sep: "__", want: m{}},
		{name: "empty name, no separator", vars: map[string]string{"": "1"}, want: m{}},
		{
			name:   "name equal to prefix",
			vars:   map[string]string{"APP_": "1", "APP_B": "2"},
			prefix: "APP_",
			sep:    "__",
			want:   m{"b": "2"},
		},
		// Present in every macOS process environment.
		{
			name: "__CF_USER_TEXT_ENCODING",
			vars: map[string]string{"__CF_USER_TEXT_ENCODING": "0x1F5:0x0:0x0", "B": "2"},
			sep:  "__",
			want: m{"b": "2"},
		},
	})
}

func TestParseVariables_UnsafeVarsAreLeftOut(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "unsafe names",
			vars: map[string]string{
				"PATH":         "/bin",
				"LD_PRELOAD":   "x",
				"GITHUB_TOKEN": "t",
				"A":            "1",
			},
			sep:  "__",
			want: m{"a": "1"},
		},
		{
			name: "matched without regard to case",
			vars: map[string]string{"path": "/bin"},
			sep:  "__",
			want: m{},
		},
		// The check runs before the prefix is removed.
		{
			name:   "prefixed unsafe name kept",
			vars:   map[string]string{"APP_PATH": "/x"},
			prefix: "APP_",
			want:   m{"path": "/x"},
		},
		{
			name:   "unsafe name matching the prefix left out",
			vars:   map[string]string{"SHELL": "/bin/sh"},
			prefix: "S",
			want:   m{},
		},
	})
}

func TestIsUnsafeVar(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"PATH", "path", "Ld_Preload", "SSH_AUTH_SOCK", "SUDO_USER", "CI"} {
		assert.True(t, env.IsUnsafeVar(name), name)
	}

	for _, name := range []string{"APP_PATH", "PATHS", "", "DATABASE_URL"} {
		assert.False(t, env.IsUnsafeVar(name), name)
	}
}

// The dynamic linker section of the list covers code-execution vectors, and
// these load code into a process the same way LD_PRELOAD and
// DYLD_INSERT_LIBRARIES do, so a `.env` file must not set them either.
func TestIsUnsafeVar_DynamicLinkerVars(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"LD_AUDIT",
		"DYLD_FRAMEWORK_PATH",
		"DYLD_FALLBACK_LIBRARY_PATH",
		"DYLD_FALLBACK_FRAMEWORK_PATH",
	} {
		assert.True(t, env.IsUnsafeVar(name), name)
	}
}

func TestParseVariables_Conflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		vars    map[string]string
		name    string
		prefix  string
		sep     string
		wantMsg string
	}{
		{
			name:    "case collision",
			vars:    map[string]string{"FOO": "1", "Foo": "2"},
			sep:     "__",
			wantMsg: `conflicting variable names: "FOO" and "Foo"`,
		},
		{
			name:    "value then map",
			vars:    map[string]string{"A": "1", "A__B": "2"},
			sep:     "__",
			wantMsg: `conflicting variable names: "A" and "A__B"`,
		},
		{
			name:    "map then value",
			vars:    map[string]string{"A__B": "1", "a": "2"},
			sep:     "__",
			wantMsg: `conflicting variable names: "A__B" and "a"`,
		},
		{
			name:    "deeper value on the way",
			vars:    map[string]string{"A__B": "1", "A__B__C": "2"},
			sep:     "__",
			wantMsg: `conflicting variable names: "A__B" and "A__B__C"`,
		},
		{
			name:    "collision after prefix",
			vars:    map[string]string{"APP_A": "1", "app_a": "2"},
			prefix:  "APP_",
			wantMsg: `conflicting variable names: "APP_A" and "app_a"`,
		},
		{
			name:    "collision without separator",
			vars:    map[string]string{"A": "1", "a": "2"},
			wantMsg: `conflicting variable names: "A" and "a"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := env.ParseVariables(tt.vars, tt.prefix, tt.sep)
			require.ErrorIs(t, err, env.ErrKeyConflict)
			assert.Nil(t, got)
			require.EqualError(t, err, tt.wantMsg)
		})
	}
}

// Names that are left out cannot conflict.
func TestParseVariables_LeftOutNamesDoNotConflict(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "unsafe",
			vars: map[string]string{"PATH": "1", "path__x": "2"},
			sep:  "__",
			want: m{"path": m{"x": "2"}},
		},
		{
			name: "empty level",
			vars: map[string]string{"A": "1", "A____B": "2"},
			sep:  "__",
			want: m{"a": "1"},
		},
		{
			name:   "outside prefix",
			vars:   map[string]string{"APP_A": "1", "a": "2"},
			prefix: "APP_",
			want:   m{"a": "1"},
		},
	})
}

// A conflict names the variables, never their values.
func TestParseVariables_ConflictOmitsValues(t *testing.T) {
	t.Parallel()

	_, err := env.ParseVariables(
		map[string]string{"TOKEN": "hunter2", "Token": "swordfish"},
		"",
		"__",
	)
	require.ErrorIs(t, err, env.ErrKeyConflict)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.NotContains(t, err.Error(), "swordfish")
}

// The same input reports the same conflict on every call, whatever order
// Go iterates the input map in.
func TestParseVariables_ConflictIsDeterministic(t *testing.T) {
	t.Parallel()

	vars := map[string]string{"A": "1", "A__B": "2", "A__C": "3", "a": "4", "B": "5", "b": "6"}
	_, first := env.ParseVariables(vars, "", "__")
	require.Error(t, first)

	for range 50 {
		_, err := env.ParseVariables(vars, "", "__")
		require.EqualError(t, err, first.Error())
	}
}

func TestParseVariables_DoesNotModifyInput(t *testing.T) {
	t.Parallel()

	vars := map[string]string{"A__B": "1", "PATH": "/bin", "__X": "2"}
	before := maps.Clone(vars)
	_, err := env.ParseVariables(vars, "", "__")
	require.NoError(t, err)
	assert.Equal(t, before, vars)
}

// FuzzParseVariables checks that ParseVariables is deterministic, returns no
// empty keys or empty nested maps, and holds no more values than it was
// given variables.
func FuzzParseVariables(f *testing.F) {
	for _, seed := range []struct{ input, prefix, sep string }{
		{"A=1\nB=2", "", "__"},
		{"A__B=1\nA__C=2", "", "__"},
		{"A=1\nA__B=2", "", "__"},
		{"FOO=1\nFoo=2", "", ""},
		{"APP_A=1\nAPP_=2", "APP_", "__"},
		{"__A=1\nA____B=2", "", "__"},
		{"A_X_B=1", "", "_X_"},
		{"PATH=/bin", "", "_"},
	} {
		f.Add(seed.input, seed.prefix, seed.sep)
	}

	f.Fuzz(func(t *testing.T, input, prefix, sep string) {
		vars := make(map[string]string)

		for line := range strings.Lines(input) {
			name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")

			vars[name] = value
		}

		got, err := env.ParseVariables(vars, prefix, sep)
		again, againErr := env.ParseVariables(vars, prefix, sep)

		if (err == nil) != (againErr == nil) || !reflect.DeepEqual(got, again) {
			t.Fatalf("ParseVariables(%q, %q, %q) is not deterministic", vars, prefix, sep)
		}

		if err != nil {
			if againErr.Error() != err.Error() {
				t.Fatalf("errors differ: %v, %v", err, againErr)
			}

			return
		}

		leaves := 0
		walk(t, got, &leaves)

		if leaves > len(vars) {
			t.Errorf(
				"ParseVariables(%q, %q, %q) has %d values from %d variables",
				vars,
				prefix,
				sep,
				leaves,
				len(vars),
			)
		}
	})
}

// walk counts the string values in data and fails on an empty key or an
// empty nested map.
func walk(t *testing.T, data m, leaves *int) {
	t.Helper()

	for k, v := range data {
		if k == "" {
			t.Errorf("empty key in %v", data)
		}

		switch v := v.(type) {
		case string:
			*leaves++
		case m:
			if len(v) == 0 {
				t.Errorf("empty nested map at %q", k)
			}

			walk(t, v, leaves)
		default:
			t.Errorf("unexpected %T at %q", v, k)
		}
	}
}
