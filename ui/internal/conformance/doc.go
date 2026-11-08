// Package conformance holds the tests of the ui.Provider contract that
// the ui service keeps over every template store: the rules of the ui
// package documentation, checked against a service over a store built by
// the caller.
//
// # Usage
//
// The service's tests call [Run] with a [Subject] that builds a store
// holding the seed files the suite gives it:
//
//	conformance.Run(t, conformance.Subject{
//		New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
//			s, err := memory.NewStore(memory.WithExtensions(".html", ".txt"))()
//			require.NoError(t, err)
//			require.NoError(t, s.RegisterFS(t.Context(), seed))
//			return s
//		},
//	})
//
// # Suite
//
// Run runs each rule as a parallel subtest, on a new service each: a
// layout with blocks, partials and escaping, and a page's top-level text
// as its block; fragments, KI-032 included; a store without layouts;
// WithBlock, concurrently too; results that do not depend on the order of
// renders, Render then WithBlock and the other way round, and each
// template escaped as a new html/template set executing it alone would;
// page names, and names that are no page or are invalid; compile
// isolation, KI-053 included, which does not fail CheckReady; names
// reserved for files; what is written on an execute error and with and
// without a status; nothing written for a page not found, a status
// refused or a block not found; RegisterFuncs adding all or nothing; the
// lifecycle sentinels and a restart that keeps registered functions;
// Reload reading the store again on every store, and seeing a change
// where the store takes one; and concurrent renders, registrations,
// reloads and readiness checks, for -race. Every error is checked with
// errors.Is and its full text.
//
// [RunMockDefaults] checks the defaults the mock of gas/ui/uitest
// documents, on a mock with every field nil:
//
//	conformance.RunMockDefaults(t, func() ui.Provider { return &uitest.MockUI{} })
package conformance
