//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Gemini App 的原生 ProcessQuery 协议没有 count_tokens 端点，凭证也不是
// OpenAI 令牌。两条 count_tokens 路径都必须在本地估算，不能发上游请求。
func TestShouldEstimateOpenAIInputTokensLocally_GeminiApp(t *testing.T) {
	account := &Account{
		Platform:    PlatformGeminiApp,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token", "refresh_token": "refresh"},
	}
	require.True(t, shouldEstimateOpenAIInputTokensLocally(account))

	// OAuth + api.openai.com base_url 的普通 OpenAI 账号仍走上游端点。
	require.False(t, shouldEstimateOpenAIInputTokensLocally(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}))
}

func TestForwardCountTokensAsAnthropic_GeminiAppEstimatesLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gemini-argon","system":"You are helpful.","messages":[{"role":"user","content":"hello world"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          301,
		Name:        "gemini-app",
		Platform:    PlatformGeminiApp,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "gemini-app-token",
			"refresh_token": "gemini-app-refresh",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, account, body, "gemini-argon")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, upstream.lastReq, "gemini_app count_tokens must be estimated locally, never forwarded")

	var payload struct {
		InputTokens int `json:"input_tokens"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Positive(t, payload.InputTokens)
}

func TestForwardCountTokensAsAnthropic_GeminiAppRejectsInvalidBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader([]byte(`not-json`)))

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{Platform: PlatformGeminiApp, Type: AccountTypeOAuth}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, account, []byte(`not-json`), "gemini-argon")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Nil(t, upstream.lastReq)
}
