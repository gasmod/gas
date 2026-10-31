// Package conformance holds the tests every store of the module runs: the
// contract of the template package, checked against a store built by the
// caller.
//
// # Usage
//
// A store's tests call [Run] with a [Subject] that builds the store over
// the seed files the suite gives it:
//
//	conformance.Run(t, conformance.Subject{
//		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
//			s, err := memory.NewStore(memory.WithExtensions(conformance.Extensions...))()
//			require.NoError(t, err)
//			require.NoError(t, s.RegisterFS(t.Context(), seed))
//			return s
//		},
//	})
//
// # Suite
//
// Run runs each rule as a parallel subtest, on a new store each: reading
// the seed, not-found and invalid names, the extension filter, List's
// order and its agreement with Get, exact case, copies, and, for a
// writable store, Register, nil content and RegisterFS, all-or-nothing
// and with a canceled context; for a read-only one, ErrReadOnly. A last
// subtest calls the store from several goroutines at once, for -race.
package conformance
