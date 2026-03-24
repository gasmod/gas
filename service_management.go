package gas

import (
	"fmt"
)

// ActiveServices returns the names of all currently active services.
func (w *Worker) ActiveServices() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := make([]string, 0, len(w.activeServices))
	for name := range w.activeServices {
		names = append(names, name)
	}
	return names
}

// CloseService performs the kill-switch sequence for a single service at
// runtime. Infrastructure is cleaned up first so that even if Close()
// panics or fails, routes and subscriptions are already removed.
func (w *Worker) CloseService(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	svc, ok := w.activeServices[name]
	if !ok {
		return fmt.Errorf("gas: service %q is not active", name)
	}

	// 1. App sets this hook to remove routes and middleware.
	if w.onServiceClose != nil {
		w.onServiceClose(name)
	}

	// 2. Remove event subscriptions.
	if w.eventBus != nil {
		w.eventBus.RemoveByModule(name)
	}

	// 3. Close the service (internal cleanup).
	if err := svc.Close(); err != nil {
		w.getLogger().Error("service close failed").Str("service", name).Err("error", err).Send()
	}

	// 4. Remove from active services.
	delete(w.activeServices, name)

	// 5. Notify all other services.
	Emit(w.eventBus, SystemServiceClosed, SystemServiceClosedPayload{ServiceName: name}).Wait()

	w.getLogger().Info("service closed").Str("service", name).Send()
	return nil
}

// RestartService re-initializes a previously closed service. The service
// must have been registered with the Worker at construction time and built
// during InitServices (i.e., it must be a singleton in serviceOrder).
func (w *Worker) RestartService(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.activeServices[name]; ok {
		return fmt.Errorf("gas: service %q is already active", name)
	}

	// Find the service in the init order (singleton instance still exists).
	var svc Service
	for _, s := range w.serviceOrder {
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

	w.activeServices[name] = svc

	Emit(w.eventBus, SystemServiceInitialized, SystemServiceInitializedPayload{ServiceName: name}).Wait()

	w.getLogger().Info("service restarted").Str("service", name).Send()
	return nil
}
