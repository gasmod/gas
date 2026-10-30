package test

import "github.com/gasmod/gas/template/dir"

// The constructor follows the CONVENTIONS.md shape: the returned function
// returns the value and an error.
var _ func() (*dir.Store, error) = dir.NewStore("")
