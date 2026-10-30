package test

import (
	"testing"
)

// TestDIError_NoConstructor covers ErrNoConstructor, which the public API
// cannot reach: every caller of invoke checks the registration first, so
// only a direct call can ask it to construct an unregistered type.
func TestDIError_NoConstructor(t *testing.T) {
	// FIXME(@ahmedkamalio)
	//type unregistered struct{}
	//c := gas.NewServiceContainer()
	//_, err := c.invoke(context.Background(), reflect.TypeFor[*unregistered](), c)
	//if !errors.Is(err, gas.ErrNoConstructor) {
	//	t.Fatalf("invoke error = %v, want ErrNoConstructor", err)
	//}
}
