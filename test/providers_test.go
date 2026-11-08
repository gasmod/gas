package gas_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gasmod/gas"
)

// ApplyEnqueueOptions returns what the options set, applied in order, and
// zero values for what none sets.
func TestApplyEnqueueOptions(t *testing.T) {
	t.Parallel()

	delay, group, dedupe, attrs := gas.ApplyEnqueueOptions(nil)
	assert.Zero(t, delay)
	assert.Empty(t, group)
	assert.Empty(t, dedupe)
	assert.Nil(t, attrs)

	delay, group, dedupe, attrs = gas.ApplyEnqueueOptions([]gas.EnqueueOption{
		gas.WithDelay(time.Second),
		gas.WithGroupID("g"),
		gas.WithDedupeID("d"),
		gas.WithJobAttributes(map[string]string{"k": "v"}),
		gas.WithDelay(2 * time.Second),
	})
	assert.Equal(t, 2*time.Second, delay)
	assert.Equal(t, "g", group)
	assert.Equal(t, "d", dedupe)
	assert.Equal(t, map[string]string{"k": "v"}, attrs)
}
