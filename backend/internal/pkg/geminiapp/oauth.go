package geminiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Google OAuth primitives for a user-supplied native application client profile.
// Mirrors native-wire/oauth-loopback.mjs and gateway/oauth-identity.mjs.

const (
	GoogleAuthorizeURL   = "https://accounts.google.com/o/oauth2/auth"
	GoogleTokenURL       = "https://oauth2.googleapis.com/token"
	GoogleNativeTokenURL = "https://www.googleapis.com/oauth2/v4/token"
	GoogleUserInfoURL    = "https://www.googleapis.com/oauth2/v3/userinfo"

	// GeminiAppAuthScope is the only authorization scope the native client uses.
	GeminiAppAuthScope = "https://www.googleapis.com/auth/flexible-api"
)

var (
	clientIDPattern = regexp.MustCompile(`^\d+-[a-z0-9]+\.apps\.googleusercontent\.com$`)
	scopePattern    = regexp.MustCompile(`^(https://www\.googleapis\.com/auth/\S+|openid|email|profile)$`)
	emailPattern    = regexp.MustCompile(`^[^\s\x00-\x1f\x7f]+@[^\s\x00-\x1f\x7f]+\.[^\s\x00-\x1f\x7f]+$`)
	subjectPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
)

// OAuthProfile is the user-supplied Google OAuth client profile.
// It is never bundled; the operator imports their own authorized profile.
type OAuthProfile struct {
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
	Scopes       []string `json:"scopes"`
	// RedirectURI must be a loopback URL registered for the client.
	RedirectURI string `json:"redirectUri"`
	// AuthScopes overrides the authorization scopes; defaults to GeminiAppAuthScope.
	AuthScopes []string `json:"authScopes,omitempty"`
	// NativeLoopback selects the v4 token endpoint (reference "native" mode).
	NativeLoopback bool `json:"nativeLoopback"`
	ForceConsent   bool `json:"forceConsent"`
}

// Validate checks the profile shape without contacting Google.
func (p *OAuthProfile) Validate() error {
	if p == nil {
		return NewError("OAUTH_PROFILE_REQUIRED", 409)
	}
	if !clientIDPattern.MatchString(strings.TrimSpace(p.ClientID)) {
		return NewError("INVALID_OAUTH_CLIENT_ID", 400)
	}
	if strings.TrimSpace(p.ClientSecret) == "" {
		return NewError("INVALID_OAUTH_CLIENT_SECRET", 400)
	}
	if len(p.Scopes) == 0 {
		return NewError("INVALID_OAUTH_PROFILE", 400)
	}
	for _, scope := range p.Scopes {
		if !scopePattern.MatchString(scope) {
			return NewErrorf("INVALID_OAUTH_PROFILE", 400, "invalid scope")
		}
	}
	// AuthScopes overrides Scopes for the authorization request, so it must pass
	// the same validation instead of only being checked at URL build time.
	for _, scope := range p.AuthScopes {
		if !scopePattern.MatchString(scope) {
			return NewErrorf("INVALID_OAUTH_PROFILE", 400, "invalid auth scope")
		}
	}
	if p.RedirectURI == "" {
		return NewError("OAUTH_REDIRECT_REQUIRED", 400)
	}
	parsed, err := url.Parse(p.RedirectURI)
	if err != nil || parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1") {
		return NewError("OAUTH_REDIRECT_MUST_BE_LOOPBACK", 400)
	}
	return nil
}

func (p *OAuthProfile) authScopes() []string {
	if len(p.AuthScopes) > 0 {
		return p.AuthScopes
	}
	return []string{GeminiAppAuthScope}
}

func (p *OAuthProfile) tokenURL() string {
	if p.NativeLoopback {
		return GoogleNativeTokenURL
	}
	return GoogleTokenURL
}

// BuildAuthorizationURL builds the Google consent URL for an imported profile.
func BuildAuthorizationURL(profile *OAuthProfile, state, codeChallenge string) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("client_id", profile.ClientID)
	values.Set("redirect_uri", profile.RedirectURI)
	values.Set("response_type", "code")
	values.Set("scope", strings.Join(profile.authScopes(), " "))
	values.Set("access_type", "offline")
	values.Set("state", state)
	values.Set("code_challenge", codeChallenge)
	values.Set("code_challenge_method", "S256")
	if profile.ForceConsent {
		values.Set("prompt", "consent")
	}
	return GoogleAuthorizeURL + "?" + values.Encode(), nil
}

// TokenResponse is the subset of the Google token response we persist.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Scope        string
}

func tokenRequest(ctx context.Context, client *http.Client, profile *OAuthProfile, form url.Values) (*TokenResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, profile.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, NewError("OAUTH_TOKEN_REQUEST_FAILED", 502)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, NewError("OAUTH_TOKEN_REQUEST_FAILED", 502)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return nil, NewError("OAUTH_TOKEN_RESPONSE_INVALID", 502)
	}
	var decoded struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    any    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, NewError("OAUTH_TOKEN_RESPONSE_INVALID", 502)
	}
	if response.StatusCode/100 != 2 || decoded.AccessToken == "" {
		code := "OAUTH_REJECTED"
		switch decoded.Error {
		case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_scope", "access_denied":
			code = decoded.Error
		}
		return nil, NewErrorf(code, 400, "oauth exchange rejected (HTTP %d)", response.StatusCode)
	}
	return &TokenResponse{
		AccessToken:  decoded.AccessToken,
		RefreshToken: decoded.RefreshToken,
		ExpiresIn:    parseExpiresIn(decoded.ExpiresIn),
		Scope:        decoded.Scope,
	}, nil
}

func parseExpiresIn(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case string:
		var parsed int64
		_, _ = fmt.Sscanf(typed, "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

// ExchangeCode exchanges an authorization code (PKCE) for tokens.
func ExchangeCode(ctx context.Context, client *http.Client, profile *OAuthProfile, code, codeVerifier string) (*TokenResponse, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", profile.ClientID)
	form.Set("redirect_uri", profile.RedirectURI)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	if profile.ClientSecret != "" {
		form.Set("client_secret", profile.ClientSecret)
	}
	return tokenRequest(ctx, client, profile, form)
}

// RefreshAccessToken refreshes an access token using the stored refresh token.
func RefreshAccessToken(ctx context.Context, client *http.Client, profile *OAuthProfile, refreshToken string) (*TokenResponse, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return nil, NewError("OAUTH_REFRESH_TOKEN_REQUIRED", 400)
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", profile.ClientID)
	form.Set("refresh_token", refreshToken)
	if profile.ClientSecret != "" {
		form.Set("client_secret", profile.ClientSecret)
	}
	if len(profile.Scopes) > 0 {
		form.Set("scope", strings.Join(profile.Scopes, " "))
	}
	return tokenRequest(ctx, client, profile, form)
}

// Identity is the verified Google account identity.
type Identity struct {
	Email   string
	Subject string
}

// FetchIdentity calls the fixed userinfo endpoint and validates the account.
func FetchIdentity(ctx context.Context, client *http.Client, accessToken string) (*Identity, error) {
	if strings.TrimSpace(accessToken) == "" || strings.ContainsAny(accessToken, "\r\n") {
		return nil, NewError("GOOGLE_IDENTITY_UNAVAILABLE", 502)
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, GoogleUserInfoURL, nil)
	if err != nil {
		return nil, NewError("GOOGLE_IDENTITY_UNAVAILABLE", 502)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.Do(request)
	if err != nil {
		return nil, NewError("GOOGLE_IDENTITY_UNAVAILABLE", 502)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32*1024))
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, NewError("GOOGLE_IDENTITY_UNAVAILABLE", 502)
	}
	var decoded struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Subject       string `json:"sub"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, NewError("GOOGLE_IDENTITY_UNAVAILABLE", 502)
	}
	if !decoded.EmailVerified || len(decoded.Email) > 254 || !emailPattern.MatchString(decoded.Email) || !subjectPattern.MatchString(decoded.Subject) {
		return nil, NewError("GOOGLE_VERIFIED_EMAIL_REQUIRED", 502)
	}
	return &Identity{Email: decoded.Email, Subject: decoded.Subject}, nil
}
