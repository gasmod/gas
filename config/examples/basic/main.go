// Example usage of the config package
package main

import (
	"fmt"
	"os"

	"github.com/gasmod/gas/config"
)

type Config struct {
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

const (
	serverDefaultHost       = "0.0.0.0"
	serverDefaultPort       = 8080
	databaseDefaultHost     = "localhost"
	databaseDefaultPort     = 5432
	databaseDefaultUser     = "admin"
	databaseDefaultPassword = "admin"
	loggingDefaultLevel     = "debug"
)

func main() {
	initEnvVars()

	// initialize config service
	cfg := config.New() // by default, it uses the env provider

	// Load configuration
	if err := cfg.Load(); err != nil {
		panic(err)
	}

	// Bind to user-defined type
	var appCfg Config
	if err := cfg.Bind(&appCfg); err != nil {
		panic(err)
	}

	validateConfig(&appCfg)

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

func initEnvVars() {
	_ = os.Setenv("SERVER__HOST", serverDefaultHost)
	_ = os.Setenv("SERVER__PORT", fmt.Sprintf("%d", serverDefaultPort))
	_ = os.Setenv("DATABASE__HOST", databaseDefaultHost)
	_ = os.Setenv("DATABASE__PORT", fmt.Sprintf("%d", databaseDefaultPort))
	_ = os.Setenv("DATABASE__USER", databaseDefaultUser)
	_ = os.Setenv("DATABASE__PASSWORD", databaseDefaultPassword)
	_ = os.Setenv("LOGGING__LEVEL", loggingDefaultLevel)
}

func validateConfig(cfg *Config) {
	if cfg.Server.Host != serverDefaultHost {
		panic("invalid server host")
	}
	if cfg.Server.Port != serverDefaultPort {
		panic("invalid server port")
	}
	if cfg.Database.Host != databaseDefaultHost {
		panic("invalid database host")
	}
	if cfg.Database.Port != databaseDefaultPort {
		panic("invalid database port")
	}
	if cfg.Database.User != databaseDefaultUser {
		panic("invalid database user")
	}
	if cfg.Database.Password != databaseDefaultPassword {
		panic("invalid database password")
	}
	if cfg.Logging.Level != loggingDefaultLevel {
		panic("invalid logging level")
	}
}
