//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountTestServiceGeminiAppGuards(t *testing.T) {
	oauthAccount := &Account{ID: 1, Platform: PlatformGeminiApp, Type: AccountTypeOAuth}
	gateway := &GeminiAppGatewayService{}

	tests := []struct {
		name    string
		svc     *AccountTestService
		account *Account
		model   string
		wantErr string
	}{
		{
			name:    "nil gateway service",
			svc:     &AccountTestService{},
			account: oauthAccount,
			wantErr: "Gemini App service is not configured",
		},
		{
			name:    "nil account",
			svc:     &AccountTestService{geminiAppGatewayService: gateway},
			account: nil,
			wantErr: "Gemini App accounts require OAuth credentials",
		},
		{
			name:    "non oauth account",
			svc:     &AccountTestService{geminiAppGatewayService: gateway},
			account: &Account{ID: 2, Platform: PlatformGeminiApp, Type: AccountTypeAPIKey},
			wantErr: "Gemini App accounts require OAuth credentials",
		},
		{
			name:    "unsupported model",
			svc:     &AccountTestService{geminiAppGatewayService: gateway},
			account: oauthAccount,
			model:   "gpt-4",
			wantErr: "Unsupported model: gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder := newTestContext()
			err := tt.svc.testGeminiAppAccountConnection(c, tt.account, tt.model, "hi")
			require.EqualError(t, err, tt.wantErr)

			body := recorder.Body.String()
			require.Contains(t, body, `"type":"error"`)
			require.Contains(t, body, tt.wantErr)
		})
	}
}

// TestAccountTestServiceGeminiAppEmitsSSELifecycle drives the only path that
// reaches the concrete *GeminiAppGatewayService: a configured gateway with no
// token provider fails after test_start has already been streamed. The concrete
// type prevents injecting a fake successful turn.
func TestAccountTestServiceGeminiAppEmitsSSELifecycle(t *testing.T) {
	svc := &AccountTestService{geminiAppGatewayService: &GeminiAppGatewayService{}}
	account := &Account{
		ID:          1,
		Platform:    PlatformGeminiApp,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "refresh-token"},
	}

	c, recorder := newTestContext()
	err := svc.testGeminiAppAccountConnection(c, account, "", "")
	require.EqualError(t, err, "gemini app token provider not configured")

	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "keep-alive", recorder.Header().Get("Connection"))
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"model":"gemini-argon"`)
	require.Contains(t, body, `"type":"error"`)
	require.Contains(t, body, "gemini app token provider not configured")
}
