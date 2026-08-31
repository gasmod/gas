// Package main shows how infrastructure modules are registered on an App.
package main

// #region imports
import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	gaslog "github.com/gasmod/gas/log"
	storages3 "github.com/gasmod/gas/storage/s3"
)

// #endregion imports

func main() {
	// #region config
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}
	// #endregion config

	// #region app
	app := gas.NewApp(
		// Infrastructure every module draws on.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Modules, registered under the provider interface your services inject.
		gas.WithSingletonService[gas.StorageProvider](storages3.New()),
	)
	// #endregion app

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
