//go:build cgo

package reflection_test

import (
	"runtime/cgo"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas/config/internal/reflection"
)

// incomplete stands for an incomplete C type, which Go cannot allocate.
type incomplete struct{ _ cgo.Incomplete }

// A pointer to a type Go cannot allocate, such as an incomplete C type,
// does not make Clone panic and is returned as it is.
func TestClone_PointerToIncompleteType(t *testing.T) {
	t.Parallel()

	//nolint:gosec // Go cannot allocate the type, so it can only be pointed at
	p := (*incomplete)(unsafe.Pointer(new([8]byte)))

	assert.NotPanics(t, func() { reflection.Clone(p) })
	assert.NotPanics(t, func() { reflection.Clone([]any{p}) })
	assert.Same(t, p, reflection.Clone(p))
}
