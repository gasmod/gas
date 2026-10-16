package gas

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestDIError_NoConstructor covers ErrNoConstructor, which the public API
// cannot reach: every caller of invoke checks the registration first, so
// only a direct call can ask it to construct an unregistered type.
func TestDIError_NoConstructor(t *testing.T) {
	type unregistered struct{}
	c := NewServiceContainer()
	_, err := c.invoke(context.Background(), reflect.TypeFor[*unregistered](), c)
	if !errors.Is(err, ErrNoConstructor) {
		t.Fatalf("invoke error = %v, want ErrNoConstructor", err)
	}
}
