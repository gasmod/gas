// Package uitest provides a mock implementation of gas.UIProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &uitest.MockUI{}
//	mock.RenderFn = func(w http.ResponseWriter, name string, data any) error {
//	    w.Write([]byte("<h1>Hello</h1>"))
//	    return nil
//	}
package uitest
