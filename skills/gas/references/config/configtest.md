# github.com/gasmod/gas/config/configtest

```go
import "github.com/gasmod/gas/config/configtest"
```

Package configtest holds `MockConfig`, a config.ConfigProvider for tests
of code that reads configuration.

## Usage

A MockConfig records each call, and calls the function in the field
named after the method, when it is set:

```go
m := &configtest.MockConfig{
	GetFn: func(key string) any { return "db" },
}
svc := NewService(m)
// m.CallCount("Get") is how many times svc called Get
```

`NewMockConfigWithValues` sets every field to the methods of a real
config.Config holding the given values, so Get, Find, Bind and the rest
behave as they do in an app:

```go
m, err := configtest.NewMockConfigWithValues(map[string]any{
	"database": map[string]any{"host": "db"},
})
```

## Calls

Each method appends a `Call` with its name and arguments to Calls, then
calls its field. With the field nil, it returns zero values: nil, false,
or a nil error. LoadProviderContext records the provider but not the
context.

The methods may be called from several goroutines at once, as Calls is
appended to under a lock. `MockConfig.CallCount` and `MockConfig.Reset`
take the lock too, but reading Calls directly does not, so read it once
the calls are done. Setting a field while a method runs is a data race.

## Values

NewMockConfigWithValues sets the values with config.Config.SetDefaults,
so their keys are trimmed and lowercased, and two keys that become the
same are an error. Nil values make an empty configuration. No provider
loads, so the environment does not reach it. Any field may be replaced
afterwards, and LoadProvider loads a provider into the configuration.

## API

Run `go doc -all github.com/gasmod/gas/config/configtest` for every declaration, or `go doc github.com/gasmod/gas/config/configtest.<Name>` for one.
