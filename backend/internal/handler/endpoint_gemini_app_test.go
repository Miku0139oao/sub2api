//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Gemini App 走原生 ProcessQuery RPC，没有任何 OpenAI 形态的上游端点；
// 无论入站是 chat/completions 还是 responses，上报的 upstream_endpoint
// 都必须是 /gemini-app/ProcessQuery。
func TestDeriveUpstreamEndpoint_GeminiApp(t *testing.T) {
	for _, inbound := range []string{
		"",
		EndpointChatCompletions,
		EndpointResponses,
		EndpointResponsesInputTokens,
		EndpointMessages,
	} {
		t.Run(inbound, func(t *testing.T) {
			require.Equal(t,
				service.GeminiAppUpstreamEndpoint,
				DeriveUpstreamEndpoint(inbound, "/gemini-app/v1/chat/completions", service.PlatformGeminiApp),
			)
		})
	}
}

func TestGetUpstreamEndpoint_GeminiApp(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("falls back to the native rpc label", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/gemini-app/v1/chat/completions", nil)

		require.Equal(t, service.GeminiAppUpstreamEndpoint, GetUpstreamEndpoint(c, service.PlatformGeminiApp))
	})

	t.Run("prefers the endpoint recorded during forwarding", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/gemini-app/v1/responses", nil)
		service.SetActualOpenAIUpstreamEndpoint(c, service.GeminiAppUpstreamEndpoint)

		require.Equal(t, service.GeminiAppUpstreamEndpoint, GetUpstreamEndpoint(c, service.PlatformGeminiApp))
	})

	// 回归：修复前 Gemini App 不在该 gate 内，recorded endpoint 会被普通入站推导覆盖。
	t.Run("recorded endpoint wins over inbound derivation", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/gemini-app/v1/chat/completions", nil)
		service.SetActualOpenAIUpstreamEndpoint(c, service.GeminiAppUpstreamEndpoint)

		require.NotEqual(t, EndpointChatCompletions, GetUpstreamEndpoint(c, service.PlatformGeminiApp))
	})
}
