// Package configtest holds [MockConfig], a config.ConfigProvider for tests
// of code that reads configuration, and [StaticProvider], a config.Provider
// that loads fixed values.
//
// # Usage
//
// A MockConfig records each call, and calls the function in the field
// named after the method, when it is set:
//
//	m := &configtest.MockConfig{
//		GetFn: func(key string) any { return "db" },
//	}
//	svc := NewService(m)
//	// m.CallCount("Get") is how many times svc called Get
//
// [NewMockConfigWithValues] sets every field to the methods of a real
// config.Config holding the given values, so Get, Find, Bind and the rest
// behave as they do in an app:
//
//	m, err := configtest.NewMockConfigWithValues(map[string]any{
//		"database": map[string]any{"host": "db"},
//	})
//
// # Calls
//
// Each method appends a [Call] with its name and arguments to Calls, then
// calls its field. With the field nil, it returns zero values: nil, false,
// or a nil error. LoadProviderContext records the provider but not the
// context.
//
// The methods may be called from several goroutines at once, as Calls is
// appended to under a lock. [MockConfig.CallCount] and [MockConfig.Reset]
// take the lock too, but reading Calls directly does not, so read it once
// the calls are done. Setting a field while a method runs is a data race.
//
// # Values
//
// NewMockConfigWithValues sets the values with config.Config.SetDefaults,
// so their keys are trimmed and lowercased, and two keys that become the
// same are an error. Nil values make an empty configuration. No provider
// loads, so the environment does not reach it. Any field may be replaced
// afterwards, and LoadProvider loads a provider into the configuration.
//
// # Static provider
//
// A [StaticProvider] gives a real config.Config fixed values, for a test
// of code that builds its own Config from providers, such as an app's
// function that loads its configuration:
//
//	c := config.New(config.WithProvider(configtest.NewStaticProvider(map[string]any{
//		"database": map[string]any{"host": "db"},
//	})))
//	if err := c.Load(); err != nil {
//		return err
//	}
//
// Each load returns the values, and the Config treats them as any
// provider's: their keys are trimmed and lowercased, two keys that become
// the same fail the load, and a later provider overrides them. Errors name
// the provider `static`. Nil values load nothing.
//
// The provider keeps the map it is given rather than copying it, so do not
// change the map while a load may run.
package configtest
