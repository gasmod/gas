// Example usage of the config package
package main

import (
	"fmt"
	"os"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
)

type AppConfig struct {
	Server struct {
		Host string
		Port int
	}
	Logging struct {
		Level string
	}
	Database struct {
		Host     string
		User     string
		Password string
		Port     int
	}
}

func initEnvVars() {
	_ = os.Setenv("MYAPP_DATABASE__HOST", "localhost")
	_ = os.Setenv("MYAPP_DATABASE__PORT", "5432")
	_ = os.Setenv("MYAPP_DATABASE__USER", "admin")
	_ = os.Setenv("MYAPP_DATABASE__PASSWORD", "admin")
	_ = os.Setenv("MYAPP_SERVER__HOST", "0.0.0.0")
	_ = os.Setenv("MYAPP_SERVER_PORT", "8080") // using the standard _ separator should also work!
	_ = os.Setenv("MYAPP_LOGGING__LEVEL", "debug")
}

func main() {
	initEnvVars()

	// initialize config service
	cfg := config.New(
		config.WithProvider(
			providers.NewEnvProvider(
				providers.WithEnvPrefix("MYAPP_"),
			),
		),
	)

	// Load configuration
	if err := cfg.Load(); err != nil {
		panic(err)
	}

	// Bind to user-defined type
	var appCfg AppConfig
	if err := cfg.Bind(&appCfg); err != nil {
		panic(err)
	}

	// Use the config
	fmt.Printf("Server: %s:%d\n", appCfg.Server.Host, appCfg.Server.Port)
	fmt.Printf(
		"DB: postgresql://%s:%s@%s:%d\n",
		appCfg.Database.User,
		appCfg.Database.Password,
		appCfg.Database.Host,
		appCfg.Database.Port,
	)
	fmt.Printf("Log Level: %s\n", appCfg.Logging.Level)
}
