package session

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestService_Close_idempotent(t *testing.T) {
	svc := &Service{done: make(chan struct{})}

	assert.NoError(t, svc.Close(context.Background()), "first Close should succeed")
	assert.NotPanics(t, func() {
		_ = svc.Close(context.Background())
	}, "second Close must not panic")
}
