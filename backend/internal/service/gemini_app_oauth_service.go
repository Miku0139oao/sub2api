package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
)

const (
	// GeminiAppOAuthProfileEnv 是 Gemini App OAuth client profile 的内联 JSON 配置。
	// 该 profile 不随项目分发，由运维方自行导入（clientId / clientSecret / scopes /
	// redirectUri 等字段与 gemini-app2api 的 import-profile 格式一致）。
	GeminiAppOAuthProfileEnv = "GEMINI_APP_OAUTH_PROFILE"
	// GeminiAppOAuthProfileFileEnv 是同一份 profile 的文件路径（二选一）。
	GeminiAppOAuthProfileFileEnv = "GEMINI_APP_OAUTH_PROFILE_FILE"

	geminiAppTokenExpirySkew = 300 // 提前 5 分钟视为过期
)

var snakeCaseKeyPattern = regexp.MustCompile(`"([a-z]+)_([a-z_]+)"\s*:`)

// GeminiAppOAuthService 负责 Gemini App 的 Google OAuth 登录、token 刷新与凭证构建。
type GeminiAppOAuthService struct {
	proxyRepo   ProxyRepository
	sessions    *antigravity.SessionStore
	loadProfile func() (*geminiapp.OAuthProfile, error)
}

// NewGeminiAppOAuthService 创建 OAuth 服务。
func NewGeminiAppOAuthService(proxyRepo ProxyRepository) *GeminiAppOAuthService {
	return &GeminiAppOAuthService{
		proxyRepo:   proxyRepo,
		sessions:    antigravity.NewSessionStore(),
		loadProfile: LoadGeminiAppOAuthProfile,
	}
}

// SetProfileLoader 覆盖 profile 读取逻辑（测试用）。
func (s *GeminiAppOAuthService) SetProfileLoader(loader func() (*geminiapp.OAuthProfile, error)) {
	if loader != nil {
		s.loadProfile = loader
	}
}

// LoadGeminiAppOAuthProfile 从环境变量或文件读取 OAuth client profile。
func LoadGeminiAppOAuthProfile() (*geminiapp.OAuthProfile, error) {
	raw := strings.TrimSpace(os.Getenv(GeminiAppOAuthProfileEnv))
	if raw == "" {
		if path := strings.TrimSpace(os.Getenv(GeminiAppOAuthProfileFileEnv)); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, geminiapp.NewErrorf("OAUTH_PROFILE_UNREADABLE", 500, "%v", err)
			}
			raw = string(data)
		}
	}
	if raw == "" {
		return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
	}
	profile, err := parseGeminiAppOAuthProfile(raw)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

func parseGeminiAppOAuthProfile(raw string) (*geminiapp.OAuthProfile, error) {
	normalized := normalizeProfileJSONKeys(raw)
	var profile geminiapp.OAuthProfile
	if err := json.Unmarshal([]byte(normalized), &profile); err != nil {
		return nil, geminiapp.NewErrorf("INVALID_OAUTH_PROFILE", 400, "%v", err)
	}
	if profile.AuthScopes == nil {
		profile.AuthScopes = []string{geminiapp.GeminiAppAuthScope}
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return &profile, nil
}

// normalizeProfileJSONKeys 接受上游 profile 的 camelCase 字段，也接受运维手写的
// snake_case 字段。
func normalizeProfileJSONKeys(raw string) string {
	return snakeCaseKeyPattern.ReplaceAllStringFunc(raw, func(match string) string {
		parts := snakeCaseKeyPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		camel := parts[1]
		for _, segment := range strings.Split(parts[2], "_") {
			if segment == "" {
				continue
			}
			camel += strings.ToUpper(segment[:1]) + segment[1:]
		}
		return `"` + camel + `":`
	})
}

func (s *GeminiAppOAuthService) profile() (*geminiapp.OAuthProfile, error) {
	if s.loadProfile == nil {
		return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
	}
	return s.loadProfile()
}

func (s *GeminiAppOAuthService) proxyURL(ctx context.Context, proxyID *int64) string {
	if proxyID == nil || s.proxyRepo == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}

// ProxyURLForID 解析代理 ID 对应的 URL（供 admin handler 使用）。
func (s *GeminiAppOAuthService) ProxyURLForID(ctx context.Context, proxyID int64) string {
	return s.proxyURL(ctx, &proxyID)
}

// GeminiAppAuthURLResult 是授权链接生成结果。
type GeminiAppAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	// RedirectURI 提示前端：登录完成后把浏览器地址栏里的 code 粘贴回来。
	RedirectURI string `json:"redirect_uri"`
}

// GeminiAppTokenInfo 是 OAuth token 结果。
type GeminiAppTokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"subject,omitempty"`
}

// GeminiAppExchangeCodeInput 是 code 交换输入。
type GeminiAppExchangeCodeInput struct {
	SessionID string
	State     string
	Code      string
	ProxyID   *int64
}

// GeminiAppCapabilities 描述当前部署的 Gemini App OAuth 可用性。
type GeminiAppCapabilities struct {
	Configured  bool   `json:"configured"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Capabilities 返回 OAuth client profile 是否已配置。
func (s *GeminiAppOAuthService) Capabilities() *GeminiAppCapabilities {
	profile, err := s.profile()
	if err != nil {
		return &GeminiAppCapabilities{
			Configured: false,
			Message:    "GEMINI_APP_OAUTH_PROFILE 未配置",
		}
	}
	return &GeminiAppCapabilities{
		Configured:  true,
		RedirectURI: profile.RedirectURI,
	}
}

// GenerateAuthURL 生成 Google OAuth 授权链接。
func (s *GeminiAppOAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64) (*GeminiAppAuthURLResult, error) {
	profile, err := s.profile()
	if err != nil {
		return nil, err
	}
	state, err := antigravity.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("生成 state 失败: %w", err)
	}
	codeVerifier, err := antigravity.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("生成 code_verifier 失败: %w", err)
	}
	sessionID, err := antigravity.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("生成 session_id 失败: %w", err)
	}
	authURL, err := geminiapp.BuildAuthorizationURL(profile, state, antigravity.GenerateCodeChallenge(codeVerifier))
	if err != nil {
		return nil, err
	}
	s.sessions.Set(sessionID, &antigravity.OAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		ProxyURL:     s.proxyURL(ctx, proxyID),
		CreatedAt:    time.Now(),
	})
	return &GeminiAppAuthURLResult{
		AuthURL:     authURL,
		SessionID:   sessionID,
		State:       state,
		RedirectURI: profile.RedirectURI,
	}, nil
}

// ExchangeCode 用 authorization code 交换 token 并读取已验证的 Google 身份。
func (s *GeminiAppOAuthService) ExchangeCode(ctx context.Context, input *GeminiAppExchangeCodeInput) (*GeminiAppTokenInfo, error) {
	if input == nil {
		return nil, geminiapp.NewError("INVALID_OAUTH_EXCHANGE", 400)
	}
	session, ok := s.sessions.Get(input.SessionID)
	if !ok {
		return nil, geminiapp.NewErrorf("OAUTH_SESSION_NOT_FOUND", http.StatusBadRequest, "session 不存在或已过期")
	}
	if strings.TrimSpace(input.State) == "" || input.State != session.State {
		return nil, geminiapp.NewErrorf("INVALID_OAUTH_STATE", http.StatusBadRequest, "state 无效")
	}
	profile, err := s.profile()
	if err != nil {
		return nil, err
	}
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		proxyURL = s.proxyURL(ctx, input.ProxyID)
	}
	client, err := geminiapp.NewHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	tokens, err := geminiapp.ExchangeCode(ctx, client, profile, strings.TrimSpace(input.Code), session.CodeVerifier)
	if err != nil {
		return nil, fmt.Errorf("token 交换失败: %w", err)
	}
	s.sessions.Delete(input.SessionID)

	info := buildGeminiAppTokenInfo(tokens)
	if identity, identityErr := geminiapp.FetchIdentity(ctx, client, tokens.AccessToken); identityErr == nil {
		info.Email = identity.Email
		info.Subject = identity.Subject
	} else {
		slog.Warn("gemini_app_identity_unavailable", "error", identityErr)
	}
	return info, nil
}

// RefreshToken 用 refresh_token 换新的 access_token。
func (s *GeminiAppOAuthService) RefreshToken(ctx context.Context, refreshToken string, proxyURL string) (*GeminiAppTokenInfo, error) {
	profile, err := s.profile()
	if err != nil {
		return nil, err
	}
	client, err := geminiapp.NewHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	tokens, err := geminiapp.RefreshAccessToken(ctx, client, profile, refreshToken)
	if err != nil {
		return nil, err
	}
	return buildGeminiAppTokenInfo(tokens), nil
}

func buildGeminiAppTokenInfo(tokens *geminiapp.TokenResponse) *GeminiAppTokenInfo {
	expiresAt := time.Now().Unix() + tokens.ExpiresIn - geminiAppTokenExpirySkew
	return &GeminiAppTokenInfo{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
		ExpiresAt:    expiresAt,
		TokenType:    "Bearer",
		Scope:        tokens.Scope,
	}
}

// BuildAccountCredentials 构建账号凭证。
func (s *GeminiAppOAuthService) BuildAccountCredentials(info *GeminiAppTokenInfo) map[string]any {
	if info == nil {
		return nil
	}
	creds := map[string]any{
		"access_token": info.AccessToken,
		"expires_at":   strconv.FormatInt(info.ExpiresAt, 10),
	}
	if info.RefreshToken != "" {
		creds["refresh_token"] = info.RefreshToken
	}
	if info.TokenType != "" {
		creds["token_type"] = info.TokenType
	}
	if info.Scope != "" {
		creds["scope"] = info.Scope
	}
	if info.Email != "" {
		creds["email"] = info.Email
	}
	if info.Subject != "" {
		creds["subject"] = info.Subject
	}
	return creds
}

// Stop 停止 session 清理协程。
func (s *GeminiAppOAuthService) Stop() {
	if s.sessions != nil {
		s.sessions.Stop()
	}
}
