//go:build unit

package service

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// geminiAppTokenCacheStub is a deterministic GeminiTokenCache for provider tests.
type geminiAppTokenCacheStub struct {
	mu sync.Mutex

	tokens   []string // successive GetAccessToken results; empty -> ""
	getCalls int

	setCalls int
	setKey   string
	setToken string
	setTTL   time.Duration

	lockAcquired bool
	lockErr      error
	lockCalls    int

	releaseCalls int
	releasedKeys []string

	deletedKeys []string
}

func (s *geminiAppTokenCacheStub) GetAccessToken(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if len(s.tokens) == 0 {
		return "", nil
	}
	token := s.tokens[0]
	s.tokens = s.tokens[1:]
	return token, nil
}

func (s *geminiAppTokenCacheStub) SetAccessToken(_ context.Context, cacheKey, token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls++
	s.setKey = cacheKey
	s.setToken = token
	s.setTTL = ttl
	return nil
}

func (s *geminiAppTokenCacheStub) DeleteAccessToken(_ context.Context, cacheKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedKeys = append(s.deletedKeys, cacheKey)
	return nil
}

func (s *geminiAppTokenCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockCalls++
	return s.lockAcquired, s.lockErr
}

func (s *geminiAppTokenCacheStub) ReleaseRefreshLock(_ context.Context, cacheKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls++
	s.releasedKeys = append(s.releasedKeys, cacheKey)
	return nil
}

func geminiAppExpiredAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "refresh-token",
			"expires_at":    strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10),
		},
	}
}

func TestGeminiAppTokenProviderGuards(t *testing.T) {
	provider := NewGeminiAppTokenProvider(nil, nil, nil, nil)

	_, err := provider.GetAccessToken(context.Background(), nil)
	require.EqualError(t, err, "account is nil")

	_, err = provider.GetAccessToken(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.EqualError(t, err, "not a gemini app oauth account")

	_, err = provider.GetAccessToken(context.Background(), &Account{Platform: PlatformGeminiApp, Type: AccountTypeAPIKey})
	require.EqualError(t, err, "not a gemini app oauth account")
}

func TestGeminiAppTokenProviderCacheHitShortCircuits(t *testing.T) {
	cache := &geminiAppTokenCacheStub{tokens: []string{"cached-token"}}
	// A non-nil oauth service would attempt a refresh if the cache were missed.
	oauth := NewGeminiAppOAuthService(nil)
	provider := NewGeminiAppTokenProvider(nil, nil, cache, oauth)

	token, err := provider.GetAccessToken(context.Background(), geminiAppExpiredAccount(7))
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
	require.Equal(t, 1, cache.getCalls)
	require.Zero(t, cache.setCalls)
}

func TestGeminiAppTokenProviderMissingRefreshTokenReturnsLoginRequired(t *testing.T) {
	cache := &geminiAppTokenCacheStub{}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, NewGeminiAppOAuthService(nil))
	account := &Account{
		ID:       7,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "stale-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10),
		},
	}

	_, err := provider.GetAccessToken(context.Background(), account)
	requireGeminiAppProtocolError(t, err, "ACCOUNT_LOGIN_REQUIRED", 401)
}

func TestGeminiAppTokenProviderEmptyAccessTokenReturnsLoginRequired(t *testing.T) {
	cache := &geminiAppTokenCacheStub{}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, NewGeminiAppOAuthService(nil))
	account := &Account{
		ID:       7,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "refresh-token",
			"expires_at":    strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}

	_, err := provider.GetAccessToken(context.Background(), account)
	requireGeminiAppProtocolError(t, err, "ACCOUNT_LOGIN_REQUIRED", 401)
}

func TestGeminiAppTokenProviderServesFreshTokenAndCachesIt(t *testing.T) {
	cache := &geminiAppTokenCacheStub{}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, nil)
	account := &Account{
		ID:       7,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "refresh-token",
			"expires_at":    strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10),
		},
	}

	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "fresh-token", token)
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, "ga:account:7", cache.setKey)
	require.Equal(t, "fresh-token", cache.setToken)
	require.Greater(t, cache.setTTL, 20*time.Minute)
	require.LessOrEqual(t, cache.setTTL, 25*time.Minute)
}

func TestGeminiAppTokenProviderRereadsCacheWhenRefreshLockNotAcquired(t *testing.T) {
	cache := &geminiAppTokenCacheStub{
		tokens:       []string{"", "peer-token"},
		lockAcquired: false,
	}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, NewGeminiAppOAuthService(nil))

	token, err := provider.GetAccessToken(context.Background(), geminiAppExpiredAccount(7))
	require.NoError(t, err)
	require.Equal(t, "peer-token", token)
	require.Equal(t, 2, cache.getCalls)
	require.Equal(t, 1, cache.lockCalls)
	require.Zero(t, cache.releaseCalls)
}

func TestGeminiAppTokenProviderExpiredWithoutOAuthServiceServesStoredToken(t *testing.T) {
	cache := &geminiAppTokenCacheStub{}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, nil)
	account := geminiAppExpiredAccount(7)

	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "stale-token", token)
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, "ga:account:7", cache.setKey)
	require.Equal(t, "stale-token", cache.setToken)
	require.Equal(t, time.Minute, cache.setTTL)
}

func TestGeminiAppTokenCacheKey(t *testing.T) {
	require.Equal(t, "ga:account:0", GeminiAppTokenCacheKey(nil))
	require.Equal(t, "ga:account:42", GeminiAppTokenCacheKey(&Account{ID: 42}))
}

func TestGeminiAppTokenProviderWaitsForConcurrentRefresh(t *testing.T) {
	// The peer holds the refresh lock and publishes a token after our first cache
	// read misses, so the provider must poll the cache instead of refreshing.
	cache := &geminiAppTokenCacheStub{tokens: []string{"", "peer-token"}, lockAcquired: false}
	// A non-nil OAuth service makes the refresh path reachable; the lock hand-off
	// must return before any network refresh happens.
	provider := NewGeminiAppTokenProvider(nil, nil, cache, NewGeminiAppOAuthService(nil))
	account := geminiAppExpiredAccount(11)

	token, err := provider.GetAccessToken(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "peer-token", token)
	require.Equal(t, 1, cache.lockCalls)
	require.Zero(t, cache.releaseCalls)
}

func TestGeminiAppTokenProviderFallsBackToRefreshWhenPeerNeverPublishes(t *testing.T) {
	// The peer holds the lock but never publishes: the provider waits out the
	// hand-off window and then attempts its own refresh, surfacing that failure.
	previous := geminiAppRefreshWaitTimeout
	geminiAppRefreshWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { geminiAppRefreshWaitTimeout = previous })

	cache := &geminiAppTokenCacheStub{lockAcquired: false}
	provider := NewGeminiAppTokenProvider(nil, nil, cache, NewGeminiAppOAuthService(nil))
	account := geminiAppExpiredAccount(12)

	start := time.Now()
	_, err := provider.GetAccessToken(context.Background(), account)

	require.Error(t, err)
	require.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
	require.Equal(t, 1, cache.lockCalls)
}
