package gas

import (
	"fmt"
)

// ActiveServices returns the names of all currently active services.
func (a *App) ActiveServices() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.activeServices))
	for name := range a.activeServices {
		names = append(names, name)
	}
	return names
}

// CloseService performs the kill-switch sequence for a single service at
// runtime. Infrastructure is cleaned up first so that even if Close()
// panics or fails, routes and subscriptions are already removed.
func (a *App) CloseService(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	svc, ok := a.activeServices[name]
	if !ok {
		return fmt.Errorf("gas: service %q is not active", name)
	}

	// 1. Remove routes and middleware.
	if a.router != nil {
		a.router.RemoveByModule(name)
	}

	// 2. Remove event subscriptions.
	if a.eventBus != nil {
		a.eventBus.RemoveByModule(name)
	}

	// 3. Close the service (internal cleanup).
	if err := svc.Close(); err != nil {
		a.getLogger().Error("service close failed").Str("service", name).Err("error", err).Send()
	}

	// 4. Remove from active services.
	delete(a.activeServices, name)

	// 5. Notify all other services.
	Emit(a.eventBus, SystemServiceClosed, SystemServiceClosedPayload{ServiceName: name}).Wait()

	a.getLogger().Info("service closed").Str("service", name).Send()
	return nil
}

// RestartService re-initializes a previously closed service. The service
// must have been registered with the App at construction time and built
// during InitServices (i.e., it must be a singleton in serviceOrder).
func (a *App) RestartService(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.activeServices[name]; ok {
		return fmt.Errorf("gas: service %q is already active", name)
	}

	// Find the service in the init order (singleton instance still exists).
	var svc Service
	for _, s := range a.serviceOrder {
		if s.Name() == name {
			svc = s
			break
		}
	}
	if svc == nil {
		return fmt.Errorf("gas: service %q not found", name)
	}

	// Re-initialize.
	if err := svc.Init(); err != nil {
		return fmt.Errorf("gas: re-init %s: %w", name, err)
	}

	a.activeServices[name] = svc

	Emit(a.eventBus, SystemServiceInitialized, SystemServiceInitializedPayload{ServiceName: name}).Wait()

	a.getLogger().Info("service restarted").Str("service", name).Send()
	return nil
}
