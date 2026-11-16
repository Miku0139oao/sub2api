//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/stretchr/testify/require"
)

func geminiAppGatewayWithProvider() *GeminiAppGatewayService {
	return &GeminiAppGatewayService{tokenProvider: &GeminiAppTokenProvider{}}
}

func decodeGeminiAppError(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok, "response must contain an error object, got %s", string(body))
	return errObj
}

func TestGeminiAppGatewayForwardGuards(t *testing.T) {
	validAccount := &Account{ID: 1, Platform: PlatformGeminiApp, Type: AccountTypeOAuth}

	tests := []struct {
		name        string
		svc         *GeminiAppGatewayService
		account     *Account
		body        string
		protocol    geminiAppProtocol
		wantStatus  int
		wantType    string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "nil account",
			svc:         geminiAppGatewayWithProvider(),
			account:     nil,
			body:        `{"model":"gemini-argon","messages":[{"role":"user","content":"hi"}]}`,
			protocol:    geminiAppProtocolChatCompletions,
			wantStatus:  http.StatusBadRequest,
			wantType:    "invalid_request_error",
			wantMessage: "account is required",
		},
		{
			name:        "wrong platform",
			svc:         geminiAppGatewayWithProvider(),
			account:     &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			body:        `{"model":"gemini-argon","messages":[{"role":"user","content":"hi"}]}`,
			protocol:    geminiAppProtocolChatCompletions,
			wantStatus:  http.StatusBadRequest,
			wantType:    "invalid_request_error",
			wantMessage: "account is not a gemini app account",
		},
		{
			name:        "non oauth account",
			svc:         geminiAppGatewayWithProvider(),
			account:     &Account{ID: 1, Platform: PlatformGeminiApp, Type: AccountTypeAPIKey},
			body:        `{"model":"gemini-argon","messages":[{"role":"user","content":"hi"}]}`,
			protocol:    geminiAppProtocolChatCompletions,
			wantStatus:  http.StatusBadRequest,
			wantType:    "invalid_request_error",
			wantMessage: "gemini app accounts require OAuth credentials",
		},
		{
			name:        "nil token provider",
			svc:         &GeminiAppGatewayService{},
			account:     validAccount,
			body:        `{"model":"gemini-argon","messages":[{"role":"user","content":"hi"}]}`,
			protocol:    geminiAppProtocolChatCompletions,
			wantStatus:  http.StatusBadGateway,
			wantType:    "upstream_error",
			wantMessage: "Gemini App is not configured",
		},
		{
			name:       "invalid json body",
			svc:        geminiAppGatewayWithProvider(),
			account:    validAccount,
			body:       `not-json`,
			protocol:   geminiAppProtocolChatCompletions,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "INVALID_TOOL_JSON",
		},
		{
			name:        "non object body",
			svc:         geminiAppGatewayWithProvider(),
			account:     validAccount,
			body:        `[1,2,3]`,
			protocol:    geminiAppProtocolChatCompletions,
			wantStatus:  http.StatusBadRequest,
			wantType:    "invalid_request_error",
			wantMessage: "request body must be a JSON object",
		},
		{
			name:       "unsupported model",
			svc:        geminiAppGatewayWithProvider(),
			account:    validAccount,
			body:       `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			protocol:   geminiAppProtocolChatCompletions,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "UNSUPPORTED_MODEL",
		},
		{
			name:       "image input",
			svc:        geminiAppGatewayWithProvider(),
			account:    validAccount,
			body:       `{"model":"gemini-argon","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
			protocol:   geminiAppProtocolChatCompletions,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "UNSUPPORTED_IMAGE_INPUT",
		},
		{
			name:       "responses image input",
			svc:        geminiAppGatewayWithProvider(),
			account:    validAccount,
			body:       `{"model":"gemini-argon","input":[{"type":"input_image","image_url":"https://example.com/a.png"}]}`,
			protocol:   geminiAppProtocolResponses,
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "UNSUPPORTED_IMAGE_INPUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder := newTestContext()
			result, err := tt.svc.forward(context.Background(), c, tt.account, []byte(tt.body), tt.protocol)
			require.Nil(t, result)
			require.Error(t, err)
			require.Equal(t, tt.wantStatus, recorder.Code)

			errObj := decodeGeminiAppError(t, recorder.Body.Bytes())
			require.Equal(t, tt.wantType, errObj["type"])
			if tt.wantMessage != "" {
				require.Equal(t, tt.wantMessage, errObj["message"])
			}
			if tt.wantCode != "" {
				require.Equal(t, tt.wantCode, errObj["code"])
			} else {
				require.NotContains(t, errObj, "code")
			}
		})
	}
}

func TestGeminiAppErrorRenderers(t *testing.T) {
	t.Run("plain chat error has type and message only", func(t *testing.T) {
		c, recorder := newTestContext()
		err := writeGeminiAppError(c, geminiAppProtocolChatCompletions, http.StatusBadRequest, "invalid_request_error", "boom")
		require.EqualError(t, err, "boom")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.True(t, IsResponseCommitted(c))

		errObj := decodeGeminiAppError(t, recorder.Body.Bytes())
		require.Equal(t, "invalid_request_error", errObj["type"])
		require.Equal(t, "boom", errObj["message"])
		require.NotContains(t, errObj, "code")
	})

	t.Run("plain responses error includes null code and param", func(t *testing.T) {
		c, recorder := newTestContext()
		_ = writeGeminiAppError(c, geminiAppProtocolResponses, http.StatusBadRequest, "invalid_request_error", "boom")
		require.Equal(t, http.StatusBadRequest, recorder.Code)

		errObj := decodeGeminiAppError(t, recorder.Body.Bytes())
		require.Equal(t, "invalid_request_error", errObj["type"])
		require.Contains(t, errObj, "code")
		require.Nil(t, errObj["code"])
		require.Contains(t, errObj, "param")
		require.Nil(t, errObj["param"])
	})

	t.Run("mapped request error uses the protocol status and localized message", func(t *testing.T) {
		c, recorder := newTestContext()
		_ = writeGeminiAppMappedError(c, geminiAppProtocolChatCompletions, geminiapp.NewError("UNSUPPORTED_IMAGE_INPUT", http.StatusBadRequest))
		require.Equal(t, http.StatusBadRequest, recorder.Code)

		errObj := decodeGeminiAppError(t, recorder.Body.Bytes())
		require.Equal(t, "invalid_request_error", errObj["type"])
		require.Equal(t, "UNSUPPORTED_IMAGE_INPUT", errObj["code"])
		require.Equal(t, "Gemini App does not accept image input", errObj["message"])
	})

	t.Run("mapped server error uses upstream_error type", func(t *testing.T) {
		c, recorder := newTestContext()
		_ = writeGeminiAppMappedError(c, geminiAppProtocolChatCompletions, geminiapp.NewError("NATIVE_TURN_FAILED", http.StatusBadGateway))
		require.Equal(t, http.StatusBadGateway, recorder.Code)

		errObj := decodeGeminiAppError(t, recorder.Body.Bytes())
		require.Equal(t, "upstream_error", errObj["type"])
		require.Equal(t, "NATIVE_TURN_FAILED", errObj["code"])
		require.Equal(t, "Gemini App request failed: NATIVE_TURN_FAILED", errObj["message"])
	})
}

func TestGeminiAppFailoverErrorClassification(t *testing.T) {
	t.Run("429 becomes retryable quota failover", func(t *testing.T) {
		failover := geminiAppFailoverError(geminiapp.NewError("NATIVE_QUOTA_EXHAUSTED", http.StatusTooManyRequests))
		require.NotNil(t, failover)
		require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
		require.Equal(t, http.StatusTooManyRequests, failover.ClientStatusCode)
		require.Equal(t, NextAccountRetry, failover.NextAccountAction)
		require.Equal(t, "Gemini App quota exhausted", failover.ClientMessage)
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run(http.StatusText(status)+" becomes credential failover", func(t *testing.T) {
			failover := geminiAppFailoverError(geminiapp.NewError("NATIVE_PERMISSION_OR_AUTH", status))
			require.NotNil(t, failover)
			require.Equal(t, http.StatusBadGateway, failover.StatusCode)
			require.Equal(t, http.StatusBadGateway, failover.ClientStatusCode)
			require.Equal(t, GatewayFailureStageAccountAuth, failover.Stage)
			require.Equal(t, GatewayFailureScopeAccount, failover.Scope)
			require.Equal(t, GeminiAppCredentialRejectedReason, failover.Reason)
			require.Equal(t, NextAccountRetry, failover.NextAccountAction)
			require.Equal(t, "Gemini App credential rejected by upstream", failover.ClientMessage)
		})
	}

	require.Nil(t, geminiAppFailoverError(geminiapp.NewError("NATIVE_TURN_FAILED", http.StatusBadRequest)))

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		status := status
		t.Run(http.StatusText(status)+" becomes retryable upstream failover", func(t *testing.T) {
			failover := geminiAppFailoverError(geminiapp.NewError("NATIVE_TURN_FAILED", status))
			require.NotNil(t, failover)
			require.Equal(t, status, failover.StatusCode)
			require.Equal(t, http.StatusBadGateway, failover.ClientStatusCode)
			require.Equal(t, NextAccountRetry, failover.NextAccountAction)
			require.Equal(t, "Gemini App upstream error", failover.ClientMessage)
		})
	}

	require.Nil(t, geminiAppFailoverError(nil))
	require.Nil(t, geminiAppFailoverError(context.Canceled))
}

func TestGeminiAppErrorMessageMapping(t *testing.T) {
	tests := map[string]string{
		"UNSUPPORTED_MODEL":          "model is not supported by Gemini App; use gemini-argon, gemini-hydrogen or gemini-flash-lite",
		"UNSUPPORTED_IMAGE_INPUT":    "Gemini App does not accept image input",
		"NATIVE_QUOTA_INSUFFICIENT":  "Gemini App quota exhausted",
		"NATIVE_PERMISSION_OR_AUTH":  "Gemini App credential rejected by upstream",
		"MODEL_DOWNGRADE_REJECTED":   "upstream served a different model than requested",
		"EMPTY_NATIVE_RESPONSE":      "upstream returned an empty response",
		"INCOMPLETE_NATIVE_RESPONSE": "upstream response was incomplete",
		"SOMETHING_ELSE":             "Gemini App request failed: SOMETHING_ELSE",
	}
	for code, want := range tests {
		require.Equal(t, want, geminiAppErrorMessage(code), code)
	}
}
