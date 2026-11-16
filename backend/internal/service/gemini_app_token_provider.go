package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
)

const (
	geminiAppTokenRefreshSkew    = 3 * time.Minute
	geminiAppTokenCacheSkew      = 5 * time.Minute
	geminiAppRefreshWaitInterval = 250 * time.Millisecond
)

// geminiAppRefreshWaitTimeout 是等待并发刷新结果的上限（测试中可缩短）。
var geminiAppRefreshWaitTimeout = 10 * time.Second

// GeminiAppTokenProvider manages access_token for Gemini App OAuth accounts.
type GeminiAppTokenProvider struct {
	accountRepo  AccountRepository
	proxyRepo    ProxyRepository
	tokenCache   GeminiTokenCache
	oauthService *GeminiAppOAuthService
}

// NewGeminiAppTokenProvider 创建 token provider。
func NewGeminiAppTokenProvider(accountRepo AccountRepository, proxyRepo ProxyRepository, tokenCache GeminiTokenCache, oauthService *GeminiAppOAuthService) *GeminiAppTokenProvider {
	return &GeminiAppTokenProvider{
		accountRepo:  accountRepo,
		proxyRepo:    proxyRepo,
		tokenCache:   tokenCache,
		oauthService: oauthService,
	}
}

// GetAccessToken 返回可用的 access_token，必要时先用 refresh_token 刷新。
func (p *GeminiAppTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGeminiApp || account.Type != AccountTypeOAuth {
		return "", errors.New("not a gemini app oauth account")
	}

	cacheKey := GeminiAppTokenCacheKey(account)
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= geminiAppTokenRefreshSkew
	if needsRefresh && p.oauthService != nil {
		refreshToken := strings.TrimSpace(account.GetCredential("refresh_token"))
		if refreshToken == "" {
			return "", geminiapp.NewError("ACCOUNT_LOGIN_REQUIRED", 401)
		}
		if p.tokenCache != nil {
			if locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second); lockErr != nil {
				slog.Warn("gemini_app_token_lock_failed", "account_id", account.ID, "error", lockErr)
			} else if !locked {
				// 另一个请求正在刷新：短暂等待它写回缓存，避免并发刷新同一账号。
				if token := waitForGeminiAppRefreshedToken(ctx, p.tokenCache, cacheKey); token != "" {
					return token, nil
				}
			} else {
				defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
			}
		}
		info, err := p.oauthService.RefreshToken(ctx, refreshToken, geminiAppProxyURL(ctx, account, p.proxyRepo))
		if err != nil {
			slog.Warn("gemini_app_token_refresh_failed", "account_id", account.ID, "error", err)
			return "", err
		}
		if account.Credentials == nil {
			account.Credentials = make(map[string]any)
		}
		account.Credentials["access_token"] = info.AccessToken
		account.Credentials["expires_at"] = strconv.FormatInt(info.ExpiresAt, 10)
		if info.RefreshToken != "" {
			account.Credentials["refresh_token"] = info.RefreshToken
		}
		if info.Scope != "" {
			account.Credentials["scope"] = info.Scope
		}
		if err := persistAccountCredentials(ctx, p.accountRepo, account, account.Credentials); err != nil {
			slog.Warn("gemini_app_token_refresh_persist_failed", "account_id", account.ID, "error", err)
		}
		expiresAt = account.GetCredentialAsTime("expires_at")
	}

	accessToken := strings.TrimSpace(account.GetCredential("access_token"))
	if accessToken == "" {
		return "", geminiapp.NewError("ACCOUNT_LOGIN_REQUIRED", 401)
	}

	if p.tokenCache != nil {
		ttl := 30 * time.Minute
		if expiresAt != nil {
			until := time.Until(*expiresAt)
			switch {
			case until > geminiAppTokenCacheSkew:
				ttl = until - geminiAppTokenCacheSkew
			case until > 0:
				ttl = until
			default:
				ttl = time.Minute
			}
		}
		_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
	}
	return accessToken, nil
}

// waitForGeminiAppRefreshedToken 在锁被其他请求持有时短暂轮询缓存，
// 让并发的请求复用同一次刷新结果；超时后返回空串，由调用方自行刷新。
func waitForGeminiAppRefreshedToken(ctx context.Context, cache GeminiTokenCache, cacheKey string) string {
	ticker := time.NewTicker(geminiAppRefreshWaitInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(geminiAppRefreshWaitTimeout)
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
			if token, err := cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				return token
			}
			if time.Now().After(deadline) {
				return ""
			}
		}
	}
}

// GeminiAppTokenCacheKey 是 Gemini App token 缓存键。
func GeminiAppTokenCacheKey(account *Account) string {
	if account == nil {
		return "ga:account:0"
	}
	return "ga:account:" + strconv.FormatInt(account.ID, 10)
}

// geminiAppProxyURL 解析账号绑定的代理 URL。
func geminiAppProxyURL(ctx context.Context, account *Account, repo ProxyRepository) string {
	if account == nil {
		return ""
	}
	if account.Proxy != nil {
		return account.Proxy.URL()
	}
	if account.ProxyID == nil || repo == nil {
		return ""
	}
	proxy, err := repo.GetByID(ctx, *account.ProxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}
