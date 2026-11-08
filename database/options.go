package database

import (
	"database/sql/driver"
	"reflect"
)

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg each time it
// runs and never changes it; the copy is checked as a bound configuration
// is. A nil cfg makes Init fail with ErrNilConfig.
//
// Default: bind the `database` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.set(cfg) }
}

// WithConnector makes ModeSQL open the database through c instead of a
// DSN, for drivers or connection setups the dialects do not cover, such
// as custom TLS or IAM authentication. d is the dialect c speaks, which
// Driver returns. With a connector, Settings.Dialect must be empty or d,
// Settings.DSN must be empty, and Settings.Mode must be ModeSQL; Init
// checks this, and that c is neither nil nor a typed nil. The service
// never closes c.
//
// Default: no connector; the dialect's driver opens Settings.DSN.
func WithConnector(d Dialect, c driver.Connector) Option {
	return func(s *Service) {
		s.connector = c
		s.connectorSpec = connectorSpec{dialect: d, set: true, isNil: isNil(c)}
	}
}

// isNil reports whether c is nil, or holds a nil pointer, map, slice,
// func or chan.
func isNil(c driver.Connector) bool {
	if c == nil {
		return true
	}

	v := reflect.ValueOf(c)

	//nolint:exhaustive // only these kinds can be nil.
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}
