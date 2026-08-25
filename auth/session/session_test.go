package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestService_Close_idempotent(t *testing.T) {
	svc := &Service{done: make(chan struct{})}

	assert.NoError(t, svc.Close(), "first Close should succeed")
	assert.NotPanics(t, func() {
		_ = svc.Close()
	}, "second Close must not panic")
}
