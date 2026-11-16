//go:build unit

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The OAuth service is a concrete *service.GeminiAppOAuthService, so the handler
// cannot be exercised with an interface stub. These tests drive the real service
// with a profile loader override (SetProfileLoader), which is the only injection
// point the service exposes. Token exchange/refresh success paths require real
// calls to Google's fixed OAuth endpoints and are therefore not covered here.

const geminiAppOAuthTestClientID = "123456789012-abcdef123456.apps.googleusercontent.com"

const (
	geminiAppOAuthCapabilitiesPath = "/api/v1/admin/gemini-app/oauth/capabilities"
	geminiAppOAuthAuthURLPath      = "/api/v1/admin/gemini-app/oauth/auth-url"
	geminiAppOAuthExchangePath     = "/api/v1/admin/gemini-app/oauth/exchange-code"
	geminiAppOAuthRefreshPath      = "/api/v1/admin/gemini-app/oauth/refresh-token"
)

type geminiAppOAuthTestEnv struct {
	handler *GeminiAppOAuthHandler
	svc     *service.GeminiAppOAuthService
	engine  *gin.Engine
}

func validGeminiAppOAuthProfile() *geminiapp.OAuthProfile {
	return &geminiapp.OAuthProfile{
		ClientID:     geminiAppOAuthTestClientID,
		ClientSecret: "test-client-secret",
		Scopes:       []string{geminiapp.GeminiAppAuthScope},
		RedirectURI:  "http://localhost:8085",
	}
}

func newGeminiAppOAuthTestEnv(t *testing.T, loader func() (*geminiapp.OAuthProfile, error)) *geminiAppOAuthTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	svc := service.NewGeminiAppOAuthService(nil)
	svc.SetProfileLoader(loader)
	t.Cleanup(svc.Stop)

	h := NewGeminiAppOAuthHandler(svc)

	engine := gin.New()
	// Mirrors internal/server/routes/admin.go: registerGeminiAppOAuthRoutes.
	admin := engine.Group("/api/v1/admin")
	geminiApp := admin.Group("/gemini-app")
	{
		geminiApp.GET("/oauth/capabilities", h.GetCapabilities)
		geminiApp.POST("/oauth/auth-url", h.GenerateAuthURL)
		geminiApp.POST("/oauth/exchange-code", h.ExchangeCode)
		geminiApp.POST("/oauth/refresh-token", h.RefreshToken)
	}

	return &geminiAppOAuthTestEnv{handler: h, svc: svc, engine: engine}
}

func (e *geminiAppOAuthTestEnv) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	e.engine.ServeHTTP(rec, req)
	return rec
}

func (e *geminiAppOAuthTestEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	e.engine.ServeHTTP(rec, req)
	return rec
}

type geminiAppOAuthEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Reason  string          `json:"reason"`
	Data    json.RawMessage `json:"data"`
}

func decodeGeminiAppOAuthEnvelope(t *testing.T, rec *httptest.ResponseRecorder) geminiAppOAuthEnvelope {
	t.Helper()
	var envelope geminiAppOAuthEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope
}

func requireGeminiAppBadRequest(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code)
	envelope := decodeGeminiAppOAuthEnvelope(t, rec)
	require.Equal(t, http.StatusBadRequest, envelope.Code)
	require.Contains(t, envelope.Message, "请求无效")
}

func TestGeminiAppOAuthCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("unconfigured profile", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
		})

		rec := env.get(t, geminiAppOAuthCapabilitiesPath)

		require.Equal(t, http.StatusOK, rec.Code)
		envelope := decodeGeminiAppOAuthEnvelope(t, rec)
		require.Equal(t, 0, envelope.Code)
		var capabilities service.GeminiAppCapabilities
		require.NoError(t, json.Unmarshal(envelope.Data, &capabilities))
		require.False(t, capabilities.Configured)
		require.Empty(t, capabilities.RedirectURI)
		require.NotEmpty(t, capabilities.Message)
	})

	t.Run("configured profile", func(t *testing.T) {
		t.Parallel()
		profile := validGeminiAppOAuthProfile()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return profile, nil
		})

		rec := env.get(t, geminiAppOAuthCapabilitiesPath)

		require.Equal(t, http.StatusOK, rec.Code)
		envelope := decodeGeminiAppOAuthEnvelope(t, rec)
		var capabilities service.GeminiAppCapabilities
		require.NoError(t, json.Unmarshal(envelope.Data, &capabilities))
		require.True(t, capabilities.Configured)
		require.Equal(t, profile.RedirectURI, capabilities.RedirectURI)
		require.Empty(t, capabilities.Message)
	})
}

func TestGeminiAppOAuthGenerateAuthURL(t *testing.T) {
	t.Parallel()

	t.Run("success payload", func(t *testing.T) {
		t.Parallel()
		profile := validGeminiAppOAuthProfile()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return profile, nil
		})

		rec := env.post(t, geminiAppOAuthAuthURLPath, `{}`)

		require.Equal(t, http.StatusOK, rec.Code)
		envelope := decodeGeminiAppOAuthEnvelope(t, rec)
		require.Equal(t, 0, envelope.Code)
		var result service.GeminiAppAuthURLResult
		require.NoError(t, json.Unmarshal(envelope.Data, &result))
		require.NotEmpty(t, result.AuthURL)
		require.True(t, strings.HasPrefix(result.AuthURL, geminiapp.GoogleAuthorizeURL+"?"), result.AuthURL)
		require.Contains(t, result.AuthURL, "code_challenge=")
		require.Contains(t, result.AuthURL, "code_challenge_method=S256")
		require.NotEmpty(t, result.SessionID)
		require.NotEmpty(t, result.State)
		require.Equal(t, profile.RedirectURI, result.RedirectURI)
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		rec := env.post(t, geminiAppOAuthAuthURLPath, `{`)

		requireGeminiAppBadRequest(t, rec)
	})

	t.Run("profile load failure keeps the upstream 409", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
		})

		rec := env.post(t, geminiAppOAuthAuthURLPath, `{}`)

		// geminiapp *Error carries its own status; the handler maps it directly.
		require.Equal(t, http.StatusConflict, rec.Code)
		envelope := decodeGeminiAppOAuthEnvelope(t, rec)
		require.Equal(t, "OAUTH_PROFILE_REQUIRED", envelope.Reason)
		require.Contains(t, envelope.Message, "OAUTH_PROFILE_REQUIRED")
	})
}

func TestGeminiAppOAuthExchangeCode(t *testing.T) {
	t.Parallel()

	t.Run("invalid JSON body", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		rec := env.post(t, geminiAppOAuthExchangePath, `not-json`)

		requireGeminiAppBadRequest(t, rec)
	})

	t.Run("missing required fields", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		for name, body := range map[string]string{
			"all missing":    `{}`,
			"missing code":   `{"session_id":"session","state":"state"}`,
			"missing state":  `{"session_id":"session","code":"code"}`,
			"missing sessID": `{"state":"state","code":"code"}`,
		} {
			t.Run(name, func(t *testing.T) {
				rec := env.post(t, geminiAppOAuthExchangePath, body)
				requireGeminiAppBadRequest(t, rec)
			})
		}
	})

	t.Run("unknown session is a client error", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		rec := env.post(t, geminiAppOAuthExchangePath,
			`{"session_id":"does-not-exist","state":"state","code":"code"}`)

		// A stale/expired session is a client error: the service returns
		// geminiapp.NewErrorf("OAUTH_SESSION_NOT_FOUND", 400, ...).
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "OAUTH_SESSION_NOT_FOUND", decodeGeminiAppOAuthEnvelope(t, rec).Reason)
	})

	t.Run("service error carrying an HTTP status", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		// Start a real session so the handler reaches the profile lookup.
		authRec := env.post(t, geminiAppOAuthAuthURLPath, `{}`)
		require.Equal(t, http.StatusOK, authRec.Code)
		var authResult service.GeminiAppAuthURLResult
		require.NoError(t, json.Unmarshal(decodeGeminiAppOAuthEnvelope(t, authRec).Data, &authResult))

		env.svc.SetProfileLoader(func() (*geminiapp.OAuthProfile, error) {
			return nil, geminiapp.NewError("OAUTH_PROFILE_UNREADABLE", 500)
		})

		rec := env.post(t, geminiAppOAuthExchangePath, fmt.Sprintf(
			`{"session_id":%q,"state":%q,"code":"auth-code"}`, authResult.SessionID, authResult.State))

		// BUG (reported): geminiapp *Error carries Status=500 here, and ErrorFrom
		// happens to yield 500 for a different reason (generic fallback), so this
		// case alone would not reveal the lost status. The profile-required case
		// above (expected 409) documents the collapse.
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestGeminiAppOAuthRefreshToken(t *testing.T) {
	t.Parallel()

	t.Run("invalid JSON body", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		rec := env.post(t, geminiAppOAuthRefreshPath, `{`)

		requireGeminiAppBadRequest(t, rec)
	})

	t.Run("blank refresh token", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return validGeminiAppOAuthProfile(), nil
		})

		for name, body := range map[string]string{
			"omitted":        `{}`,
			"empty string":   `{"refresh_token":""}`,
			"invalid fields": `{"refresh_token":42}`,
		} {
			t.Run(name, func(t *testing.T) {
				rec := env.post(t, geminiAppOAuthRefreshPath, body)
				requireGeminiAppBadRequest(t, rec)
			})
		}
	})

	t.Run("profile load failure is surfaced through ErrorFrom", func(t *testing.T) {
		t.Parallel()
		env := newGeminiAppOAuthTestEnv(t, func() (*geminiapp.OAuthProfile, error) {
			return nil, errors.New("profile missing")
		})

		rec := env.post(t, geminiAppOAuthRefreshPath, `{"refresh_token":"refresh-token"}`)

		// The error is raised before any HTTP client is built, so this is deterministic.
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Equal(t, "internal error", decodeGeminiAppOAuthEnvelope(t, rec).Message)
	})
}
