// Package templatetest provides a mock implementation of gas.TemplateProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &templatetest.MockTemplate{}
//	mock.GetFn = func(ctx context.Context, name string) ([]byte, error) {
//	    return []byte("<h1>Hello</h1>"), nil
//	}
package templatetest
