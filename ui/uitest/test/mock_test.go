package uitest_test

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/ui"
	"github.com/gasmod/gas/ui/internal/conformance"
	"github.com/gasmod/gas/ui/uitest"
)

// errBoom is what the mock's fields return.
var errBoom = errors.New("boom")

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := httptest.NewRecorder()

	m := &uitest.MockUI{
		RenderFn: func(_ context.Context, w http.ResponseWriter, name string, _ any, _ ...ui.RenderOption) error {
			_, err := io.WriteString(w, "<h1>"+name+"</h1>")

			return err
		},
	}
	err := m.Render(ctx, w, "home", nil, ui.WithStatus(http.StatusAccepted))
	require.NoError(t, err)
	assert.Equal(t, "<h1>home</h1>", w.Body.String())
	assert.Equal(t, 1, m.CallCount("Render"))
}

// With its fields nil, the mock writes nothing and returns nil, for any
// arguments.
func TestMock_Defaults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &uitest.MockUI{}
	w := httptest.NewRecorder()

	require.NoError(t, m.Render(ctx, w, "", nil, nil))
	require.NoError(t, m.RenderFragment(ctx, w, "../x", nil, ui.WithStatus(-1)))
	require.NoError(t, m.RegisterFuncs(template.FuncMap{"bad-name": nil}))
	require.NoError(t, m.RegisterFuncs(nil))
	assert.Equal(t, 0, w.Body.Len())
	assert.Empty(t, w.Header())
}

// The mock keeps the defaults its package documents, as the
// conformance suite of the defaults checks them.
func TestMock_DefaultsSuite(t *testing.T) {
	t.Parallel()

	conformance.RunMockDefaults(t, func() ui.Provider { return &uitest.MockUI{} })
}

// Each method returns what its field returns, with the arguments it got.
func TestMock_Fields(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	boom := errBoom

	var got []any

	m := &uitest.MockUI{
		RenderFn: func(_ context.Context, _ http.ResponseWriter, name string, data any, opts ...ui.RenderOption) error {
			got = append(got, name, data, len(opts))

			return boom
		},
		RenderFragmentFn: func(_ context.Context, _ http.ResponseWriter, name string, _ any, _ ...ui.RenderOption) error {
			got = append(got, name)

			return boom
		},
		RegisterFuncsFn: func(funcs template.FuncMap) error {
			got = append(got, len(funcs))

			return boom
		},
	}

	require.ErrorIs(t, m.Render(ctx, nil, "a", 1, ui.WithLayout("l")), boom)
	require.ErrorIs(t, m.RenderFragment(ctx, nil, "b", nil), boom)
	require.ErrorIs(t, m.RegisterFuncs(template.FuncMap{"f": nil}), boom)
	assert.Equal(t, []any{"a", 1, 1, "b", 1}, got)
}

// Calls records each call's arguments after the context and the writer,
// options resolved and function maps copied, in order; changing what it
// returns changes no recorded call; CallCount and Reset work on them.
func TestMock_Calls(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &uitest.MockUI{}
	funcs := template.FuncMap{"f": "x"}

	require.NoError(t, m.Render(ctx, nil, "a", "d", ui.WithLayout("l"), ui.WithStatus(201)))
	require.NoError(t, m.RenderFragment(ctx, nil, "b", nil, ui.WithBlock("x"), nil))
	require.NoError(t, m.RegisterFuncs(funcs))
	require.NoError(t, m.RegisterFuncs(nil))

	funcs["g"] = "y"

	calls := m.Calls()
	assert.Equal(t, []uitest.Call{
		{Method: "Render", Args: []any{"a", "d", ui.RenderOptions{Layout: "l", Status: 201}}},
		{Method: "RenderFragment", Args: []any{"b", nil, ui.RenderOptions{Block: "x"}}},
		{Method: "RegisterFuncs", Args: []any{template.FuncMap{"f": "x"}}},
		{Method: "RegisterFuncs", Args: []any{template.FuncMap(nil)}},
	}, calls)

	calls[2].Args[0].(template.FuncMap)["h"] = "z"
	calls[0].Args[0] = "changed"
	assert.Equal(t, template.FuncMap{"f": "x"}, m.Calls()[2].Args[0])
	assert.Equal(t, "a", m.Calls()[0].Args[0])

	assert.Equal(t, 2, m.CallCount("RegisterFuncs"))
	assert.Equal(t, 0, m.CallCount("Nope"))

	m.Reset()
	assert.Empty(t, m.Calls())
}

// The mock may be called and read from several goroutines at once; -race
// checks it, where reading the exported Calls field once raced.
func TestMock_Concurrent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	m := &uitest.MockUI{}

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = m.Render(ctx, nil, "a", nil)
				_ = m.RenderFragment(ctx, nil, "a", nil)
				_ = m.RegisterFuncs(template.FuncMap{"f": "x"})
				_ = m.Calls()
				_ = m.CallCount("Render")
			}
		})
	}

	wg.Go(func() {
		for range 50 {
			m.Reset()
		}
	})

	wg.Wait()
}
