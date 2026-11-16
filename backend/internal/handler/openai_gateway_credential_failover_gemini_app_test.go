//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 默认兜底文案是 Grok 专用（"No healthy Grok OAuth account"）。Gemini App
// 的凭证拒绝必须走自己的分支，否则客户端会把 Gemini 的失败误读成 Grok 账号问题。
func TestCredentialFailoverClientResponse_GeminiApp(t *testing.T) {
	t.Run("uses typed client status and message", func(t *testing.T) {
		status, message := credentialFailoverClientResponse(&service.UpstreamFailoverError{
			Reason:           service.GeminiAppCredentialRejectedReason,
			ClientStatusCode: http.StatusUnauthorized,
			ClientMessage:    "Gemini App session expired, re-authorize the account",
		})
		require.Equal(t, http.StatusUnauthorized, status)
		require.Equal(t, "Gemini App session expired, re-authorize the account", message)
	})

	t.Run("defaults to 502 and the gemini app constant", func(t *testing.T) {
		status, message := credentialFailoverClientResponse(&service.UpstreamFailoverError{
			Reason: service.GeminiAppCredentialRejectedReason,
		})
		require.Equal(t, http.StatusBadGateway, status)
		require.Equal(t, service.GeminiAppCredentialRejectedClientMessage, message)
		require.NotContains(t, message, "Grok")
	})

	t.Run("blank client message still yields the gemini app constant", func(t *testing.T) {
		status, message := credentialFailoverClientResponse(&service.UpstreamFailoverError{
			Reason:        service.GeminiAppCredentialRejectedReason,
			ClientMessage: "   ",
		})
		require.Equal(t, http.StatusBadGateway, status)
		require.Equal(t, service.GeminiAppCredentialRejectedClientMessage, message)
	})

	t.Run("other reasons keep the grok fallback", func(t *testing.T) {
		status, message := credentialFailoverClientResponse(&service.UpstreamFailoverError{})
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.Equal(t, service.GrokCredentialUnavailableClientMessage, message)
	})
}
