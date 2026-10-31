// Package templatetest holds a mock of gas.TemplateProvider,
// [MockTemplate], for tests of code that reads or registers templates.
//
// # Usage
//
// The mock records each call, and returns what the function in the field
// named after the method returns:
//
//	m := &templatetest.MockTemplate{
//		GetFn: func(context.Context, string) ([]byte, error) {
//			return []byte("<p>hi</p>"), nil
//		},
//	}
//	b, err := m.Get(ctx, "home.html")
//	// b is <p>hi</p>, err is nil, and m.CallCount("Get") is 1
//
// # Defaults
//
// With a field nil, its method behaves as an empty store: Get returns an
// error wrapping template.ErrTemplateNotFound, `template: not found:`
// and the quoted name; List returns an empty, non-nil list; Register and
// RegisterFS return nil.
//
// # Calls
//
// Each method appends a [Call] with its name and its arguments after the
// context, before it calls its field: the name for Get, none for List, the
// name and the content for Register, the fs.FS for RegisterFS. The content
// is recorded as a copy, nil for nil, so that a caller that reuses its
// slice does not change a recorded call. Calls returns a copy of the calls
// in the order they were made, CallCount counts those of one method, and
// Reset forgets them all.
//
// The methods may be called from several goroutines at once, as the calls
// are kept under a lock, which Calls, CallCount and Reset take too.
// Setting a field while a method runs is a data race.
package templatetest
