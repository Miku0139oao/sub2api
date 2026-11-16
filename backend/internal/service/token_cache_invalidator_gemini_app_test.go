//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompositeTokenCacheInvalidator_GeminiApp(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)
	account := &Account{
		ID:          42,
		Platform:    PlatformGeminiApp,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "refresh-token"},
	}

	require.NoError(t, invalidator.InvalidateToken(context.Background(), account))
	require.Equal(t, []string{"ga:account:42"}, cache.deletedKeys)
	require.Equal(t, []string{GeminiAppTokenCacheKey(account)}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_GeminiAppLeavesOtherPlatformsUntouched(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache)

	accounts := []*Account{
		{ID: 1, Platform: PlatformGeminiApp, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 4, Platform: PlatformGeminiApp, Type: AccountTypeAPIKey},
	}
	for _, account := range accounts {
		require.NoError(t, invalidator.InvalidateToken(context.Background(), account))
	}

	// Only the OAuth Gemini App account invalidates a key; other platforms keep
	// their own keys and the API-key account is skipped entirely.
	require.Equal(t, []string{"ga:account:1", "openai:account:2", "claude:account:3"}, cache.deletedKeys)
}
