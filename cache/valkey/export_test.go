package valkey

// SetTestHookBeforeCall sets the hook that each cache operation runs after
// its open check and before it uses the client.
func SetTestHookBeforeCall(s *Service, hook func()) {
	s.testHookBeforeCall = hook
}
