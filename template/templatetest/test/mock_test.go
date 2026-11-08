package templatetest_test

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/templatetest"
)

// errBoom is a failure the fields return.
var errBoom = errors.New("boom")

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	m := &templatetest.MockTemplate{
		GetFn: func(context.Context, string) ([]byte, error) {
			return []byte("<p>hi</p>"), nil
		},
	}

	b, err := m.Get(t.Context(), "home.html")
	require.NoError(t, err)
	assert.Equal(t, "<p>hi</p>", string(b))
	assert.Equal(t, 1, m.CallCount("Get"))
}

// With its fields nil the mock behaves as an empty store.
func TestMockTemplate_Defaults(t *testing.T) {
	t.Parallel()

	m := &templatetest.MockTemplate{}
	ctx := t.Context()

	_, err := m.Get(ctx, "a.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
	require.EqualError(t, err, `template: not found: "a.html"`)

	names, err := m.List(ctx)
	require.NoError(t, err)
	assert.NotNil(t, names)
	assert.Empty(t, names)

	require.NoError(t, m.Register(ctx, "a.html", nil))
	require.NoError(t, m.RegisterFS(ctx, fstest.MapFS{}))
}

// Each method records its call before it returns what its field
// returns; Calls returns a copy, CallCount counts and Reset forgets.
func TestMockTemplate_Calls(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{}
	content := []byte("x")
	m := &templatetest.MockTemplate{
		GetFn:        func(context.Context, string) ([]byte, error) { return nil, errBoom },
		ListFn:       func(context.Context) ([]string, error) { return nil, errBoom },
		RegisterFn:   func(context.Context, string, []byte) error { return errBoom },
		RegisterFSFn: func(context.Context, fs.FS) error { return errBoom },
	}
	ctx := t.Context()

	_, err := m.Get(ctx, "a.html")
	require.ErrorIs(t, err, errBoom)

	_, err = m.List(ctx)
	require.ErrorIs(t, err, errBoom)
	require.ErrorIs(t, m.Register(ctx, "b.html", content), errBoom)
	require.ErrorIs(t, m.RegisterFS(ctx, fsys), errBoom)

	// The caller reuses its slice; the recorded content is a copy.
	content[0] = 'y'

	calls := m.Calls()
	assert.Equal(t, []templatetest.Call{
		{Method: "Get", Args: []any{"a.html"}},
		{Method: "List"},
		{Method: "Register", Args: []any{"b.html", []byte("x")}},
		{Method: "RegisterFS", Args: []any{fsys}},
	}, calls)

	calls[0].Method = "changed"
	assert.Equal(t, "Get", m.Calls()[0].Method)
	assert.Equal(t, 1, m.CallCount("List"))

	m.Reset()
	assert.Empty(t, m.Calls())
	assert.Zero(t, m.CallCount("Get"))
}

// The mock may be called from several goroutines while Calls reads, under
// -race.
func TestMockTemplate_Concurrent(t *testing.T) {
	t.Parallel()

	m := &templatetest.MockTemplate{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_, _ = m.List(context.Background())
				_ = m.Calls()
			}
		})
	}

	wg.Wait()
	assert.Equal(t, 400, m.CallCount("List"))
}
