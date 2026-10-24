package projectknownissues_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gaslog "github.com/gasmod/gas/log"
)

// Docs: project/known-issues.mdx#logging
// "The option stores the value as given, and the first log call then panics
// with `makeslice: cap out of range`."
func TestKI041NegativeCapacityPanicsOnFirstLog(t *testing.T) {
	t.Parallel()
	l, err := gaslog.NewSlogLogger(
		gaslog.WithSlogInstance(slog.New(slog.NewTextHandler(io.Discard, nil))),
		gaslog.WithEventInitialCapacity(-1),
	)()
	if err != nil {
		t.Fatalf("NewSlogLogger with a negative capacity error = %v, want it accepted", err)
	}
	if msg := panicText(func() { l.Info("first").Send() }); !strings.Contains(msg, "makeslice: cap out of range") {
		t.Fatalf("first log call panic = %q, want makeslice: cap out of range", msg)
	}
}

// Docs: project/known-issues.mdx#logging
// "**Workaround:** pass zero or a positive capacity."
func TestKI041WorkaroundNonNegativeCapacity(t *testing.T) {
	t.Parallel()
	for _, c := range []int{0, 1, 16} {
		l, err := gaslog.NewSlogLogger(
			gaslog.WithSlogInstance(slog.New(slog.NewTextHandler(io.Discard, nil))),
			gaslog.WithEventInitialCapacity(c),
		)()
		if err != nil {
			t.Fatal(err)
		}
		if msg := panicText(func() { l.Info("ok").Str("k", "v").Int("n", 1).Send() }); msg != "" {
			t.Errorf("capacity %d: log call panicked with %q", c, msg)
		}
	}
}

// Docs: project/known-issues.mdx#logging
// "`log.Handler` is exported, but its fields are unexported and no constructor
// returns one"
// Checked on the log package source: no exported function returns Handler or
// *Handler.
func TestKI042HandlerNotConstructible(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[gaslog.Handler]()
	if typ.NumField() == 0 {
		t.Fatalf("log.Handler has no fields")
	}
	for f := range typ.Fields() {
		if f.IsExported() {
			t.Errorf("log.Handler field %s is exported", f.Name)
		}
	}
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "log", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Results == nil {
				continue
			}
			for _, r := range fn.Type.Results.List {
				expr := r.Type
				if star, ok := expr.(*ast.StarExpr); ok {
					expr = star.X
				}
				if id, ok := expr.(*ast.Ident); ok && id.Name == "Handler" {
					t.Errorf("%s returns a log.Handler", fn.Name.Name)
				}
			}
		}
	}
}

// Docs: project/known-issues.mdx#logging
// "**Workaround:** use `log.NewShippingHandler`, which returns a `slog.Handler`."
func TestKI042WorkaroundNewShippingHandler(t *testing.T) {
	t.Parallel()
	h, closer := gaslog.NewShippingHandler("http://127.0.0.1:1/logs", gaslog.NewOTLPMarshaler(), gaslog.WithoutLocalHandler())
	t.Cleanup(func() { _ = closer.Close() })
	slog.New(h).Info("composable")
}
