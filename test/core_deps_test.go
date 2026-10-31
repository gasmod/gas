package gas_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Modules meet only through core, so core depends on none of them, config
// included: core defines ConfigProvider and Environment, and
// *config.Config satisfies ConfigProvider structurally. The root module
// requires no gas module, and no package file imports one, so it builds
// with GOWORK=off.
func TestCoreRequiresNoGasModule(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../go.mod")
	require.NoError(t, err)

	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "require" {
			fields = fields[1:]
		}

		if len(fields) > 0 {
			assert.False(t, strings.HasPrefix(fields[0], "github.com/gasmod/gas/"),
				"go.mod requires %s", fields[0])
		}
	}

	files, err := filepath.Glob("../*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			assert.False(t, strings.HasPrefix(path, "github.com/gasmod/gas/"),
				"%s imports %s", file, path)
		}
	}
}
