package op

import (
	"context"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyCache(t *testing.T) {
	ctx := context.Background()

	// Clear cache
	apiKeyCache.Clear()
	apiKeyIDMap.Clear()

	// Test APIKeyCreate
	apikey := &model.APIKey{
		ID:     1,
		Name:   "test-key",
		APIKey: "sk-test123",
	}

	// Manually populate cache (simulating create)
	apiKeyCache.Set(apikey.ID, *apikey)
	apiKeyIDMap.Set(apikey.APIKey, apikey.ID)

	// Test APIKeyList
	keys, err := APIKeyList(ctx)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.Equal(t, "test-key", keys[0].Name)

	// Test APIKeyGet
	fetched, err := APIKeyGet(apikey.ID, ctx)
	require.NoError(t, err)
	assert.Equal(t, apikey.Name, fetched.Name)
	assert.Equal(t, apikey.APIKey, fetched.APIKey)

	// Test APIKeyGetByAPIKey
	found, err := APIKeyGetByAPIKey(apikey.APIKey, ctx)
	require.NoError(t, err)
	assert.Equal(t, apikey.ID, found.ID)
	assert.Equal(t, apikey.Name, found.Name)

	// Test cache miss
	_, err = APIKeyGet(999, ctx)
	assert.Error(t, err)

	_, err = APIKeyGetByAPIKey("non-existent", ctx)
	assert.Error(t, err)
}

func TestAPIKeyGetByAPIKeyNotFound(t *testing.T) {
	ctx := context.Background()

	apiKeyCache.Clear()
	apiKeyIDMap.Clear()

	// Test cache miss scenarios
	_, err := APIKeyGet(999, ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = APIKeyGetByAPIKey("sk-nonexistent", ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
