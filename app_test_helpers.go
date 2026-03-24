package gas

// ActiveServicesMap returns a map of all currently active services.
// This is a test helper.
func (w *Worker) ActiveServicesMap() map[string]Service {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]Service, len(w.activeServices))
	for name, svc := range w.activeServices {
		out[name] = svc
	}
	return out
}
