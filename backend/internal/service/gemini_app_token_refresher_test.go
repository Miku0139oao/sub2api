//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/stretchr/testify/require"
)

func geminiAppRefresherAccount(expiresIn time.Duration) *Account {
	return &Account{
		ID:       7,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    strconv.FormatInt(time.Now().Add(expiresIn).Unix(), 10),
		},
	}
}

func TestGeminiAppTokenRefresherCanRefresh(t *testing.T) {
	refresher := NewGeminiAppTokenRefresher(nil, nil)

	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "nil account", account: nil, want: false},
		{name: "wrong platform", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "rt"}}, want: false},
		{name: "wrong type", account: &Account{Platform: PlatformGeminiApp, Type: AccountTypeAPIKey, Credentials: map[string]any{"refresh_token": "rt"}}, want: false},
		{name: "missing refresh token", account: &Account{Platform: PlatformGeminiApp, Type: AccountTypeOAuth}, want: false},
		{name: "blank refresh token", account: &Account{Platform: PlatformGeminiApp, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "   "}}, want: false},
		{name: "eligible", account: &Account{Platform: PlatformGeminiApp, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "rt"}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, refresher.CanRefresh(tt.account))
		})
	}
}

func TestGeminiAppTokenRefresherNeedsRefresh(t *testing.T) {
	refresher := NewGeminiAppTokenRefresher(nil, nil)
	const window = 10 * time.Minute

	tests := []struct {
		name    string
		account *Account
		window  time.Duration
		want    bool
	}{
		{name: "nil account", account: nil, window: window, want: false},
		{name: "missing refresh token", account: &Account{Credentials: map[string]any{"access_token": "at"}}, window: window, want: false},
		{name: "missing access token", account: &Account{Credentials: map[string]any{"refresh_token": "rt"}}, window: window, want: true},
		{name: "missing expires_at", account: &Account{Credentials: map[string]any{"refresh_token": "rt", "access_token": "at"}}, window: window, want: true},
		{name: "far future", account: geminiAppRefresherAccount(time.Hour), window: window, want: false},
		{name: "inside window", account: geminiAppRefresherAccount(5 * time.Minute), window: window, want: true},
		{name: "past expiry", account: geminiAppRefresherAccount(-time.Minute), window: window, want: true},
		{name: "small window is clamped up", account: geminiAppRefresherAccount(8 * time.Minute), window: time.Minute, want: true},
		{name: "clamped window keeps far future off", account: geminiAppRefresherAccount(15 * time.Minute), window: time.Minute, want: false},
		{name: "large window", account: geminiAppRefresherAccount(9 * time.Minute), window: 20 * time.Minute, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, refresher.NeedsRefresh(tt.account, tt.window))
		})
	}
}

func TestGeminiAppTokenRefresherCacheKey(t *testing.T) {
	refresher := NewGeminiAppTokenRefresher(nil, nil)
	require.Equal(t, "ga:account:0", refresher.CacheKey(nil))
	require.Equal(t, "ga:account:7", refresher.CacheKey(&Account{ID: 7}))
}

func TestGeminiAppTokenRefresherRefreshGuards(t *testing.T) {
	t.Run("nil refresher", func(t *testing.T) {
		var refresher *GeminiAppTokenRefresher
		_, err := refresher.Refresh(context.Background(), &Account{})
		require.EqualError(t, err, "gemini app oauth service is not configured")
	})

	t.Run("nil oauth service", func(t *testing.T) {
		refresher := &GeminiAppTokenRefresher{}
		_, err := refresher.Refresh(context.Background(), &Account{})
		require.EqualError(t, err, "gemini app oauth service is not configured")
	})

	t.Run("missing refresh token", func(t *testing.T) {
		refresher := NewGeminiAppTokenRefresher(NewGeminiAppOAuthService(nil), nil)
		_, err := refresher.Refresh(context.Background(), &Account{
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"refresh_token": "   "},
		})
		require.EqualError(t, err, "gemini app refresh token missing")
	})
}

// TestGeminiAppTokenRefresherRefreshProfileError exercises Refresh's error path
// without reaching Google: the profile loader fails before any HTTP request.
func TestGeminiAppTokenRefresherRefreshProfileError(t *testing.T) {
	oauth := NewGeminiAppOAuthService(nil)
	oauth.SetProfileLoader(func() (*geminiapp.OAuthProfile, error) {
		return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
	})
	refresher := NewGeminiAppTokenRefresher(oauth, nil)

	creds, err := refresher.Refresh(context.Background(), geminiAppRefresherAccount(time.Minute))
	require.Nil(t, creds)
	requireGeminiAppProtocolError(t, err, "OAUTH_PROFILE_REQUIRED", 409)
}

// TestGeminiAppTokenRefresherCredentialMergeContract pins the credential merge
// rules that Refresh relies on. Refresh itself cannot be driven end-to-end in
// unit tests because geminiapp.RefreshAccessToken hardcodes Google's token
// endpoint (see report), so the pure credential helpers are asserted instead.
func TestGeminiAppTokenRefresherCredentialMergeContract(t *testing.T) {
	svc := &GeminiAppOAuthService{}
	oldCreds := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    "1",
		"scope":         "old-scope",
	}

	t.Run("upstream omits refresh token keeps the old one", func(t *testing.T) {
		newCreds := svc.BuildAccountCredentials(&GeminiAppTokenInfo{
			AccessToken: "new-access",
			ExpiresAt:   42,
			TokenType:   "Bearer",
		})
		require.NotContains(t, newCreds, "refresh_token")

		merged := MergeCredentials(oldCreds, newCreds)
		require.Equal(t, "new-access", merged["access_token"])
		require.Equal(t, "42", merged["expires_at"])
		require.Equal(t, "Bearer", merged["token_type"])
		require.Equal(t, "old-refresh", merged["refresh_token"])
		require.Equal(t, "old-scope", merged["scope"])
	})

	t.Run("upstream returns a refresh token replaces the old one", func(t *testing.T) {
		newCreds := svc.BuildAccountCredentials(&GeminiAppTokenInfo{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresAt:    43,
			TokenType:    "Bearer",
			Scope:        "new-scope",
		})

		merged := MergeCredentials(oldCreds, newCreds)
		require.Equal(t, "new-refresh", merged["refresh_token"])
		require.Equal(t, "new-scope", merged["scope"])
	})
}

func TestGeminiAppTokenRefresherRefreshRejectsNilAccount(t *testing.T) {
	refresher := NewGeminiAppTokenRefresher(NewGeminiAppOAuthService(nil), nil)

	creds, err := refresher.Refresh(context.Background(), nil)

	require.Error(t, err)
	require.Nil(t, creds)
	require.Contains(t, err.Error(), "account is required")
}
