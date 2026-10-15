# Conventions

## Built-in GAS modules

### Constructor

Every built-in GAS module should have a constructor function that initializes the module and returns an instance of the
module. The constructor function must have the following signature:

```go
func NewModule(opts ...Option) func(<dependencies>) (*Module, error)
```

`<dependencies>` are the module's dependencies, declared as constructor parameters, for example
`func(gas.ConfigProvider, gas.Logger) (*Module, error)`. The service container resolves them, so its cycle and lifetime
checks see every dependency. Constructors never take `*gas.ServiceContainer`.

This convention covers constructors whose result is registered in the DI container. Values an app builds directly, such as
`config.New`, the `log` constructors, config providers, `ui.NewEngine`, `log.NewOTLPMarshaler`, and `gas.NewNopLogger`,
are out of scope.

One-time setup belongs in the constructor, and Init must be restartable.

Interfaces and data types shared between modules live in the core.

An interface with a single owner lives in the package that owns it.
