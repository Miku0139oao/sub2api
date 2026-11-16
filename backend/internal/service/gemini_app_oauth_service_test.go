//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/stretchr/testify/require"
)

const (
	testGeminiAppClientID     = "123456789012-abc123def.apps.googleusercontent.com"
	testGeminiAppClientSecret = "test-client-secret"
	testGeminiAppRedirectURI  = "http://localhost:18765"
)

func geminiAppProfileJSON(clientID string) string {
	return `{"context":2,"enableFst":true,"clientId":"` + clientID +
		`","clientSecret":"` + testGeminiAppClientSecret + `","scopes":["openid","email"],` +
		`"redirectUri":"` + testGeminiAppRedirectURI + `"}`
}

// requireGeminiAppProtocolError asserts err is a geminiapp protocol error with
// the expected code and HTTP status.
func requireGeminiAppProtocolError(t *testing.T, err error, code string, status int) {
	t.Helper()
	require.Error(t, err)
	appErr, ok := geminiapp.AsError(err)
	require.True(t, ok, "expected geminiapp protocol error, got %v", err)
	require.Equal(t, code, appErr.Code)
	require.Equal(t, status, appErr.Status)
}

func TestLoadGeminiAppOAuthProfileFromInlineEnv(t *testing.T) {
	t.Setenv(GeminiAppOAuthProfileEnv, geminiAppProfileJSON(testGeminiAppClientID))
	t.Setenv(GeminiAppOAuthProfileFileEnv, "")

	profile, err := LoadGeminiAppOAuthProfile()
	require.NoError(t, err)
	require.Equal(t, testGeminiAppClientID, profile.ClientID)
	require.Equal(t, testGeminiAppClientSecret, profile.ClientSecret)
	require.Equal(t, testGeminiAppRedirectURI, profile.RedirectURI)
	require.Equal(t, []string{"openid", "email"}, profile.Scopes)
}

func TestLoadGeminiAppOAuthProfileFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	require.NoError(t, os.WriteFile(path, []byte(geminiAppProfileJSON(testGeminiAppClientID)), 0o600))

	t.Setenv(GeminiAppOAuthProfileEnv, "")
	t.Setenv(GeminiAppOAuthProfileFileEnv, path)

	profile, err := LoadGeminiAppOAuthProfile()
	require.NoError(t, err)
	require.Equal(t, testGeminiAppClientID, profile.ClientID)
	require.Equal(t, testGeminiAppRedirectURI, profile.RedirectURI)
}

func TestLoadGeminiAppOAuthProfileInlineTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	fileClientID := "222222222222-fileprofile.apps.googleusercontent.com"
	require.NoError(t, os.WriteFile(path, []byte(geminiAppProfileJSON(fileClientID)), 0o600))

	t.Setenv(GeminiAppOAuthProfileEnv, geminiAppProfileJSON(testGeminiAppClientID))
	t.Setenv(GeminiAppOAuthProfileFileEnv, path)

	profile, err := LoadGeminiAppOAuthProfile()
	require.NoError(t, err)
	require.Equal(t, testGeminiAppClientID, profile.ClientID)
}

func TestLoadGeminiAppOAuthProfileErrors(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		t.Setenv(GeminiAppOAuthProfileEnv, "")
		t.Setenv(GeminiAppOAuthProfileFileEnv, "")
		_, err := LoadGeminiAppOAuthProfile()
		requireGeminiAppProtocolError(t, err, "OAUTH_PROFILE_REQUIRED", 409)
	})

	t.Run("file unreadable", func(t *testing.T) {
		t.Setenv(GeminiAppOAuthProfileEnv, "")
		t.Setenv(GeminiAppOAuthProfileFileEnv, filepath.Join(t.TempDir(), "does-not-exist.json"))
		_, err := LoadGeminiAppOAuthProfile()
		requireGeminiAppProtocolError(t, err, "OAUTH_PROFILE_UNREADABLE", 500)
	})

	t.Run("inline invalid json", func(t *testing.T) {
		t.Setenv(GeminiAppOAuthProfileEnv, `{"clientId":`)
		t.Setenv(GeminiAppOAuthProfileFileEnv, "")
		_, err := LoadGeminiAppOAuthProfile()
		requireGeminiAppProtocolError(t, err, "INVALID_OAUTH_PROFILE", 400)
	})

	t.Run("file invalid json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profile.json")
		require.NoError(t, os.WriteFile(path, []byte("not-json"), 0o600))
		t.Setenv(GeminiAppOAuthProfileEnv, "")
		t.Setenv(GeminiAppOAuthProfileFileEnv, path)
		_, err := LoadGeminiAppOAuthProfile()
		requireGeminiAppProtocolError(t, err, "INVALID_OAUTH_PROFILE", 400)
	})
}

// TestParseGeminiAppOAuthProfileEdgeCases covers cases not already asserted by
// gemini_app_gateway_test.go (snake_case normalization and invalid client id).
func TestParseGeminiAppOAuthProfileEdgeCases(t *testing.T) {
	t.Run("missing scopes is rejected", func(t *testing.T) {
		raw := `{"clientId":"` + testGeminiAppClientID + `","clientSecret":"` + testGeminiAppClientSecret +
			`","redirectUri":"` + testGeminiAppRedirectURI + `"}`
		_, err := parseGeminiAppOAuthProfile(raw)
		requireGeminiAppProtocolError(t, err, "INVALID_OAUTH_PROFILE", 400)
	})

	t.Run("missing auth scopes defaults to flexible api", func(t *testing.T) {
		raw := `{"clientId":"` + testGeminiAppClientID + `","clientSecret":"` + testGeminiAppClientSecret +
			`","scopes":["openid","email"],"redirectUri":"` + testGeminiAppRedirectURI + `"}`
		profile, err := parseGeminiAppOAuthProfile(raw)
		require.NoError(t, err)
		require.Equal(t, []string{geminiapp.GeminiAppAuthScope}, profile.AuthScopes)
	})

	t.Run("missing redirect uri is rejected", func(t *testing.T) {
		raw := `{"clientId":"` + testGeminiAppClientID + `","clientSecret":"` + testGeminiAppClientSecret +
			`","scopes":["openid"]}`
		_, err := parseGeminiAppOAuthProfile(raw)
		requireGeminiAppProtocolError(t, err, "OAUTH_REDIRECT_REQUIRED", 400)
	})

	t.Run("non loopback redirect uri is rejected", func(t *testing.T) {
		raw := `{"clientId":"` + testGeminiAppClientID + `","clientSecret":"` + testGeminiAppClientSecret +
			`","scopes":["openid"],"redirectUri":"https://example.com/callback"}`
		_, err := parseGeminiAppOAuthProfile(raw)
		requireGeminiAppProtocolError(t, err, "OAUTH_REDIRECT_MUST_BE_LOOPBACK", 400)
	})

	t.Run("blank client secret is rejected", func(t *testing.T) {
		raw := `{"clientId":"` + testGeminiAppClientID + `","clientSecret":"   "` +
			`,"scopes":["openid"],"redirectUri":"` + testGeminiAppRedirectURI + `"}`
		_, err := parseGeminiAppOAuthProfile(raw)
		requireGeminiAppProtocolError(t, err, "INVALID_OAUTH_CLIENT_SECRET", 400)
	})

	t.Run("snake case native loopback keys are normalized", func(t *testing.T) {
		raw := `{"client_id":"` + testGeminiAppClientID + `","client_secret":"` + testGeminiAppClientSecret +
			`","scopes":["openid"],"redirect_uri":"` + testGeminiAppRedirectURI + `","native_loopback":true,"force_consent":true}`
		profile, err := parseGeminiAppOAuthProfile(raw)
		require.NoError(t, err)
		require.True(t, profile.NativeLoopback)
		require.True(t, profile.ForceConsent)
		require.Equal(t, testGeminiAppClientID, profile.ClientID)
	})

	t.Run("malformed json is rejected", func(t *testing.T) {
		_, err := parseGeminiAppOAuthProfile(`{oops`)
		requireGeminiAppProtocolError(t, err, "INVALID_OAUTH_PROFILE", 400)
	})
}

func TestGeminiAppOAuthServiceCapabilitiesConfigured(t *testing.T) {
	svc := NewGeminiAppOAuthService(nil)
	svc.SetProfileLoader(func() (*geminiapp.OAuthProfile, error) {
		return &geminiapp.OAuthProfile{RedirectURI: testGeminiAppRedirectURI}, nil
	})

	caps := svc.Capabilities()
	require.True(t, caps.Configured)
	require.Equal(t, testGeminiAppRedirectURI, caps.RedirectURI)
	require.Empty(t, caps.Message)
}

func TestGeminiAppOAuthServiceCapabilitiesNilLoader(t *testing.T) {
	svc := NewGeminiAppOAuthService(nil)
	svc.loadProfile = nil

	caps := svc.Capabilities()
	require.False(t, caps.Configured)
	require.Empty(t, caps.RedirectURI)
	require.NotEmpty(t, caps.Message)
}

func TestGeminiAppBuildAccountCredentials(t *testing.T) {
	svc := &GeminiAppOAuthService{}

	t.Run("full token info preserves every field", func(t *testing.T) {
		creds := svc.BuildAccountCredentials(&GeminiAppTokenInfo{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			ExpiresAt:    1700000000,
			TokenType:    "Bearer",
			Scope:        "openid email",
			Email:        "user@example.com",
			Subject:      "subject_123",
		})
		require.Equal(t, map[string]any{
			"access_token":  "access-token",
			"expires_at":    "1700000000",
			"refresh_token": "refresh-token",
			"token_type":    "Bearer",
			"scope":         "openid email",
			"email":         "user@example.com",
			"subject":       "subject_123",
		}, creds)
	})

	t.Run("optional fields are omitted when empty", func(t *testing.T) {
		creds := svc.BuildAccountCredentials(&GeminiAppTokenInfo{
			AccessToken: "access-token",
			ExpiresAt:   42,
		})
		require.Equal(t, map[string]any{
			"access_token": "access-token",
			"expires_at":   "42",
		}, creds)
	})

	t.Run("nil info returns nil", func(t *testing.T) {
		require.Nil(t, svc.BuildAccountCredentials(nil))
	})
}

func TestBuildGeminiAppTokenInfoAppliesExpirySkew(t *testing.T) {
	before := time.Now().Unix()
	info := buildGeminiAppTokenInfo(&geminiapp.TokenResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		Scope:        "openid",
	})
	after := time.Now().Unix()

	require.Equal(t, "access-token", info.AccessToken)
	require.Equal(t, "refresh-token", info.RefreshToken)
	require.Equal(t, int64(3600), info.ExpiresIn)
	require.Equal(t, "Bearer", info.TokenType)
	require.Equal(t, "openid", info.Scope)
	require.GreaterOrEqual(t, info.ExpiresAt, before+3600-geminiAppTokenExpirySkew)
	require.LessOrEqual(t, info.ExpiresAt, after+3600-geminiAppTokenExpirySkew)
}

func TestGeminiAppOAuthServiceRefreshTokenRequiresProfile(t *testing.T) {
	svc := NewGeminiAppOAuthService(nil)
	svc.SetProfileLoader(func() (*geminiapp.OAuthProfile, error) {
		return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
	})

	_, err := svc.RefreshToken(context.Background(), "refresh-token", "")
	requireGeminiAppProtocolError(t, err, "OAUTH_PROFILE_REQUIRED", 409)
}
