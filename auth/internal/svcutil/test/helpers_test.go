package svcutil_test

// binderFunc is a svcutil.Binder that calls itself.
type binderFunc func(dest any) error

// Bind calls f.
func (f binderFunc) Bind(dest any) error { return f(dest) }
