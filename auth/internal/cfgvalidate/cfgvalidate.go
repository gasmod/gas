// Package cfgvalidate checks an auth service's configuration against its
// validate struct tags, for a config that did not come through
// gas.ConfigProvider.Bind (which runs the same check itself).
package cfgvalidate

import (
	"sync"

	"github.com/go-playground/validator/v10"
)

// validate is built on first use and shared, since a *validator.Validate
// caches each struct's parsed tags.
var validate = sync.OnceValue(func() *validator.Validate { return validator.New() })

// Struct validates cfg against its validate tags.
func Struct(cfg any) error {
	return validate().Struct(cfg) //nolint:wrapcheck // callers add the context
}
