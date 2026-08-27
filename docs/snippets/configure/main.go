// Package main shows configuration loading and binding.
package main

// #region imports
import (
	"log"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	gasenv "github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/config/providers"
)

// #endregion imports

// #region load
// Config is built and loaded before the app, so a bad configuration fails
// before anything else starts. Later providers override earlier ones.
func load() *config.Config {
	cfg := config.New(
		config.WithProvider(providers.NewEnvProvider()), // lowest priority
		config.WithProvider(providers.NewJSONProvider( // overrides env
			providers.WithJSONFilePath("config.json"),
		)),
		config.WithExtension(gasenv.NewExtension()),
	)

	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}
	return cfg
}

// #endregion load

// #region register
// Registered as an instance, so every module can bind its own settings.
func register(cfg *config.Config) gas.Option {
	return gas.WithServiceInstance[gas.ConfigProvider](cfg)
}

// #endregion register

// #region bind
// Bind maps configuration into a struct, matching on json tags first and then
// case-insensitive field names, and validates it on the way through.
type AppConfig struct {
	gasenv.WithGasEnv

	Database struct {
		Host string `json:"host"`
		Port int    `json:"port"           validate:"required"`
	} `json:"database"`

	RequestTimeout time.Duration `json:"request_timeout"`
}

func bind(cfg *config.Config) (*AppConfig, error) {
	var out AppConfig
	if err := cfg.Bind(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// #endregion bind

// #region env
// Environment variables are lowercased and flat by default. Use a double
// underscore to nest: DATABASE__HOST becomes database.host.
func prefixed() *providers.EnvProvider {
	return providers.NewEnvProvider(
		// The prefix is stripped literally, so include the trailing separator.
		providers.WithEnvPrefix("APP_"),
		providers.WithEnvSeparator("__"),
	)
}

// #endregion env

// #region server
// The server settings Gas itself reads. DefaultConfig fills these in; override
// only what you need.
func serverConfig() *gas.Config {
	c := gas.DefaultConfig()
	c.Server.Port = 9090
	c.Server.ShutdownTimeout = 30 * time.Second
	return c
}

// #endregion server

func main() { _ = load() }
