package gas

import "testing"

// TestHandler_LeavesTheServerUnbuilt pins that Handler never builds the
// server: after it, the server is still unbuilt, so Start's bind is free to
// build it from the bound config. TestHandler_DoesNotBuildTheServer checks
// the outcome, which a server built early and patched at bind would also
// pass.
func TestHandler_LeavesTheServerUnbuilt(t *testing.T) {
	t.Parallel()
	app := NewApp()
	if app.Handler() == nil {
		t.Fatal("Handler() = nil")
	}
	if app.srv != nil {
		t.Error("Handler() built the server")
	}
}
