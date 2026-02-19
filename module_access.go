package gas

import (
	config "github.com/gasmod/gas-config"
)

// FindModuleByName retrieves a module by its name from the list of all registered modules.
// Returns nil if not found.
func (a *App) FindModuleByName(name string) Module {
	if name == "" {
		return nil
	}
	for _, m := range a.modules {
		if m.Name() == name {
			return m
		}
	}
	return nil
}

// FindActiveModuleByName searches for an active module by its name and returns it.
// Returns nil if the name is empty or not found.
func (a *App) FindActiveModuleByName(name string) Module {
	if name == "" {
		return nil
	}
	return a.activeModules[name]
}

// GetMigrationManager retrieves the active MigrationManager instance for the App
// or returns nil if unavailable.
func (a *App) GetMigrationManager() MigrationManager {
	if a.migrationManagerModuleName == "" {
		return nil
	}

	mod := a.FindActiveModuleByName(a.migrationManagerModuleName)
	if mod == nil {
		return nil
	}

	mgr, ok := mod.(MigrationManager)
	if !ok {
		return nil
	}

	return mgr
}

// GetConfigProvider retrieves the active configuration provider module if available or returns nil otherwise.
func (a *App) GetConfigProvider() *config.Config {
	if a.configModuleName == "" {
		return nil
	}

	mod := a.FindActiveModuleByName(a.configModuleName)
	if mod == nil {
		return nil
	}

	cfg, ok := mod.(*config.Config)
	if !ok {
		return nil
	}

	return cfg
}
