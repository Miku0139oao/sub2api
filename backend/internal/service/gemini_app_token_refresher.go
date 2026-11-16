package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

// geminiAppBackgroundRefreshSkew keeps access tokens warm ahead of expiry.
const geminiAppBackgroundRefreshSkew = 10 * time.Minute

// GeminiAppTokenRefresher refreshes Gemini App OAuth access tokens in the background.
type GeminiAppTokenRefresher struct {
	oauthService *GeminiAppOAuthService
	proxyRepo    ProxyRepository
}

func NewGeminiAppTokenRefresher(oauthService *GeminiAppOAuthService, proxyRepo ProxyRepository) *GeminiAppTokenRefresher {
	return &GeminiAppTokenRefresher{oauthService: oauthService, proxyRepo: proxyRepo}
}

func (r *GeminiAppTokenRefresher) CacheKey(account *Account) string {
	return GeminiAppTokenCacheKey(account)
}

func (r *GeminiAppTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformGeminiApp &&
		account.Type == AccountTypeOAuth &&
		strings.TrimSpace(account.GetCredential("refresh_token")) != ""
}

func (r *GeminiAppTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil || strings.TrimSpace(account.GetCredential("refresh_token")) == "" {
		return false
	}
	if strings.TrimSpace(account.GetCredential("access_token")) == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	if refreshWindow < geminiAppBackgroundRefreshSkew {
		refreshWindow = geminiAppBackgroundRefreshSkew
	}
	return time.Until(*expiresAt) < refreshWindow
}

func (r *GeminiAppTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.oauthService == nil {
		return nil, errors.New("gemini app oauth service is not configured")
	}
	if account == nil {
		return nil, errors.New("account is required")
	}
	refreshToken := strings.TrimSpace(account.GetCredential("refresh_token"))
	if refreshToken == "" {
		return nil, errors.New("gemini app refresh token missing")
	}
	proxyURL := geminiAppProxyURL(ctx, account, r.proxyRepo)
	tokenInfo, err := r.oauthService.RefreshToken(ctx, refreshToken, proxyURL)
	if err != nil {
		return nil, err
	}
	newCredentials := r.oauthService.BuildAccountCredentials(tokenInfo)
	if strings.TrimSpace(tokenInfo.RefreshToken) == "" {
		// Google 通常不轮换 refresh_token；保留旧值。
		newCredentials["refresh_token"] = refreshToken
	}
	return MergeCredentials(account.Credentials, newCredentials), nil
}

// RegisterGeminiAppRefresher adds the Gemini App background refresher.
// Must be called during construction, before Start.
func (s *TokenRefreshService) RegisterGeminiAppRefresher(refresher *GeminiAppTokenRefresher) {
	if s == nil || refresher == nil {
		return
	}
	for _, registration := range s.registrations {
		if registration.platform == PlatformGeminiApp {
			return
		}
	}
	s.registrations = append(s.registrations, tokenRefreshRegistration{
		platform:  PlatformGeminiApp,
		refresher: refresher,
		executor:  refresher,
	})
}
