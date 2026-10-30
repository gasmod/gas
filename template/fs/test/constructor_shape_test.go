package test

import (
	"testing/fstest"

	"github.com/gasmod/gas/template/fs"
)

// The constructor follows the CONVENTIONS.md shape: the returned function
// returns the value and an error.
var _ func() (*fs.Store, error) = fs.NewStore(fstest.MapFS{})
