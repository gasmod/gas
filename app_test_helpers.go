package gas

// ActiveServicesMap returns a map of all currently active services.
// This is a test helper.
func (a *App) ActiveServicesMap() map[string]Service {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]Service, len(a.activeServices))
	for name, svc := range a.activeServices {
		out[name] = svc
	}
	return out
}
