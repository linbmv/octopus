package snowflake

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnowflakeGeneration(t *testing.T) {
	// Generate multiple IDs
	id1 := GenerateID()
	id2 := GenerateID()
	id3 := GenerateID()

	// IDs should be unique
	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, id2, id3)
	assert.NotEqual(t, id1, id3)

	// IDs should be positive
	assert.Greater(t, id1, int64(0))
	assert.Greater(t, id2, int64(0))
	assert.Greater(t, id3, int64(0))
}

func TestSnowflakeConcurrency(t *testing.T) {
	const goroutines = 100
	const idsPerGoroutine = 100

	ids := make(chan int64, goroutines*idsPerGoroutine)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < idsPerGoroutine; j++ {
				ids <- GenerateID()
			}
		}()
	}

	seen := make(map[int64]bool)
	for i := 0; i < goroutines*idsPerGoroutine; i++ {
		id := <-ids
		assert.False(t, seen[id], "duplicate ID generated: %d", id)
		seen[id] = true
	}
}

func TestSnowflakeOrdering(t *testing.T) {
	// Generate IDs sequentially
	ids := make([]int64, 10)
	for i := 0; i < 10; i++ {
		ids[i] = GenerateID()
	}

	// IDs should be roughly increasing (may not be strictly monotonic due to clock adjustments)
	for i := 1; i < len(ids); i++ {
		assert.GreaterOrEqual(t, ids[i], ids[i-1])
	}
}
