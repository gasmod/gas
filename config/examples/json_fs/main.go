// Example usage of the config package
package main

import (
	"fmt"
	"testing/fstest"

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

func main() {
	fsys := fstest.MapFS{
		"config.json": &fstest.MapFile{
			Data: []byte(`{
			  "database": {
				"host": "localhost",
				"port": 5432,
				"user": "admin",
				"password": "admin"
			  },
			  "server": {
				"host": "0.0.0.0",
				"port": 8080
			  },
			  "logging": {
				"level": "debug"
			  }
			}`),
		},
	}

	// initialize config service
	cfg := config.New(
		config.WithProvider(
			providers.NewJSONProvider(
				providers.WithJSONFilePath("config.json"),
				providers.WithJSONFileFS(&fsys),
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
