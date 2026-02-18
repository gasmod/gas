package gas

import "fmt"

// ActiveModules returns the names of all currently active modules.
func (a *App) ActiveModules() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.activeModules))
	for name := range a.activeModules {
		names = append(names, name)
	}
	return names
}

// CloseModule performs the kill-switch sequence for a single module at
// runtime. Infrastructure is cleaned up first so that even if Close()
// panics or fails, routes and subscriptions are already removed.
func (a *App) CloseModule(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	mod, ok := a.activeModules[name]
	if !ok {
		return fmt.Errorf("gas: module %q is not active", name)
	}

	// 1. Remove routes and middleware (replaces routes with 503 handlers).
	if a.router != nil {
		a.router.RemoveByModule(name)
	}

	// 2. Remove event subscriptions.
	if a.eventBus != nil {
		a.eventBus.RemoveByModule(name)
	}

	// 4. Close the module (internal cleanup).
	if err := mod.Close(); err != nil {
		a.cfg.Logger.Error("module close failed", "module", name, "error", err)
	}

	// 5. Remove from active modules.
	delete(a.activeModules, name)

	// 6. Notify all other modules.
	if a.eventBus != nil {
		a.eventBus.Emit(SystemModuleClosed, NewEventData().
			Set("module_name", name))
	}

	a.cfg.Logger.Info("module closed", "module", name)
	return nil
}

// RestartModule re-initializes a previously closed module. The module
// must have been registered with the App at construction time.
func (a *App) RestartModule(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.activeModules[name]; ok {
		return fmt.Errorf("gas: module %q is already active", name)
	}

	// Find the module in the full registration list.
	var mod Module
	for _, m := range a.modules {
		if m.Name() == name {
			mod = m
			break
		}
	}
	if mod == nil {
		return fmt.Errorf("gas: module %q not found", name)
	}

	// Re-initialize.
	if err := mod.Init(); err != nil {
		return fmt.Errorf("gas: re-init %s: %w", name, err)
	}

	a.activeModules[name] = mod

	// Notify all modules.
	if a.eventBus != nil {
		a.eventBus.Emit(SystemModuleInitialized, NewEventData().
			Set("module_name", name))
	}

	a.cfg.Logger.Info("module restarted", "module", name)
	return nil
}
