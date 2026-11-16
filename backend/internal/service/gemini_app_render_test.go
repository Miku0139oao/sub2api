//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGeminiAppStreamRenderer(t *testing.T, protocol geminiAppProtocol, model string) (*geminiAppRenderer, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)

	renderer := newGeminiAppRenderer(protocol, c, &geminiapp.CanonicalRequest{Model: model}, time.Now(), "test-response-id")
	renderer.stream = true
	require.NoError(t, renderer.beginStream())
	return renderer, recorder
}

func geminiAppTestToolCall() *geminiapp.ToolCall {
	return &geminiapp.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: geminiapp.ToolFunction{
			Name:      "lookup",
			Arguments: `{"q":"x"}`,
		},
	}
}

func TestGeminiAppRenderBeginStreamHeaders(t *testing.T) {
	renderer, recorder := newGeminiAppStreamRenderer(t, geminiAppProtocolChatCompletions, geminiapp.ModelArgon)

	require.True(t, renderer.streamBegun)
	require.NotNil(t, renderer.sse)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "keep-alive", recorder.Header().Get("Connection"))
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.Empty(t, recorder.Body.String(), "beginStream must not write a body before the first chunk")
}

func TestGeminiAppRenderChatStreamContentAndToolCalls(t *testing.T) {
	renderer, recorder := newGeminiAppStreamRenderer(t, geminiAppProtocolChatCompletions, geminiapp.ModelArgon)
	renderer.includeUsage = true

	require.NoError(t, renderer.emit(geminiapp.ToolEvent{Type: "content", Text: "Hello"}))
	require.NoError(t, renderer.emit(geminiapp.ToolEvent{Type: "call", Call: geminiAppTestToolCall()}))

	_, err := renderer.finish(&geminiAppTurnResult{
		Content: "Hello",
		Calls:   []geminiapp.ToolCall{*geminiAppTestToolCall()},
	}, "prompt")
	require.NoError(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, `"object":"chat.completion.chunk"`)
	require.Contains(t, body, `"delta":{"role":"assistant"}`)
	require.Contains(t, body, `"delta":{"content":"Hello"}`)
	require.Contains(t, body, `"tool_calls":[`)
	require.Contains(t, body, `"id":"call_1"`)
	require.Contains(t, body, `"name":"lookup"`)
	require.Contains(t, body, `"arguments":"{\"q\":\"x\"}"`)
	require.Contains(t, body, `"finish_reason":"tool_calls"`)
	require.Contains(t, body, `"prompt_tokens"`)
	require.Contains(t, body, "data: [DONE]")

	// Every chat data line is valid JSON except the terminal [DONE] marker.
	dataLines := 0
	for _, line := range strings.Split(body, "\n\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataLines++
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		require.True(t, json.Valid([]byte(payload)), "invalid SSE JSON: %s", payload)
	}
	require.GreaterOrEqual(t, dataLines, 5)
}

func TestGeminiAppRenderResponsesStreamLifecycle(t *testing.T) {
	renderer, recorder := newGeminiAppStreamRenderer(t, geminiAppProtocolResponses, geminiapp.ModelHydrogen)
	call := geminiAppTestToolCall()

	require.NoError(t, renderer.emit(geminiapp.ToolEvent{Type: "content", Text: "Hi"}))
	require.NoError(t, renderer.emit(geminiapp.ToolEvent{Type: "call", Call: call}))
	_, err := renderer.finish(&geminiAppTurnResult{
		Content: "Hi",
		Calls:   []geminiapp.ToolCall{*call},
	}, "prompt")
	require.NoError(t, err)

	body := recorder.Body.String()
	for _, event := range []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.completed",
	} {
		require.Contains(t, body, "event: "+event, event)
	}

	// Lifecycle ordering: created -> text delta -> function args -> completed.
	created := strings.Index(body, "event: response.created")
	delta := strings.Index(body, "event: response.output_text.delta")
	args := strings.Index(body, "event: response.function_call_arguments.delta")
	completed := strings.Index(body, "event: response.completed")
	require.True(t, created >= 0 && created < delta)
	require.True(t, delta < args)
	require.True(t, args < completed)

	for _, line := range strings.Split(body, "\n\n") {
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		parts := strings.SplitN(line, "\n", 2)
		require.Len(t, parts, 2)
		data := strings.TrimPrefix(parts[1], "data: ")
		require.True(t, json.Valid([]byte(data)), "invalid responses SSE JSON: %s", data)
	}
}

func TestGeminiAppRenderChatNonStreamToolCallsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	renderer := newGeminiAppRenderer(geminiAppProtocolChatCompletions, c, &geminiapp.CanonicalRequest{
		Model: geminiapp.ModelArgon,
	}, time.Now(), "chatcmpl-call")
	_, err := renderer.finish(&geminiAppTurnResult{
		Calls: []geminiapp.ToolCall{*geminiAppTestToolCall()},
	}, "prompt")
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "chat.completion", payload["object"])
	choice := payload["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "tool_calls", choice["finish_reason"])
	message := choice["message"].(map[string]any)
	require.Nil(t, message["content"])
	toolCalls := message["tool_calls"].([]any)
	require.Len(t, toolCalls, 1)
	toolCall := toolCalls[0].(map[string]any)
	require.Equal(t, "call_1", toolCall["id"])
	require.Equal(t, "function", toolCall["type"])
	require.Equal(t, "lookup", toolCall["function"].(map[string]any)["name"])
}

func TestGeminiAppRenderResponsesNonStreamToolCallsOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	renderer := newGeminiAppRenderer(geminiAppProtocolResponses, c, &geminiapp.CanonicalRequest{
		Model: geminiapp.ModelFlashLite,
	}, time.Now(), "resp_call")
	_, err := renderer.finish(&geminiAppTurnResult{
		Calls: []geminiapp.ToolCall{*geminiAppTestToolCall()},
	}, "prompt")
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "response", payload["object"])
	require.Equal(t, "completed", payload["status"])
	outputs := payload["output"].([]any)
	require.Len(t, outputs, 1)
	call := outputs[0].(map[string]any)
	require.Equal(t, "function_call", call["type"])
	require.Equal(t, "call_1", call["call_id"])
	require.Equal(t, "lookup", call["name"])
	require.Equal(t, `{"q":"x"}`, call["arguments"])
	require.Equal(t, "completed", call["status"])
}

func TestGeminiAppRenderFailKeepsFailoverBeforeFirstWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	renderer := newGeminiAppRenderer(geminiAppProtocolChatCompletions, c, &geminiapp.CanonicalRequest{
		Model: geminiapp.ModelArgon,
	}, time.Now(), "chatcmpl-fail")

	err := renderer.fail(geminiapp.NewError("NATIVE_PERMISSION_OR_AUTH", http.StatusUnauthorized))
	failover, ok := err.(*UpstreamFailoverError)
	require.True(t, ok, "expected failover error, got %T", err)
	require.Equal(t, http.StatusBadGateway, failover.StatusCode)
	require.Equal(t, GeminiAppCredentialRejectedReason, failover.Reason)
	require.Empty(t, recorder.Body.String(), "nothing must be written before failover is returned")
}

func TestGeminiAppRenderStreamErrorAfterWrite(t *testing.T) {
	renderer, recorder := newGeminiAppStreamRenderer(t, geminiAppProtocolChatCompletions, geminiapp.ModelArgon)
	require.NoError(t, renderer.emit(geminiapp.ToolEvent{Type: "content", Text: "partial"}))

	upstreamErr := geminiapp.NewError("NATIVE_QUOTA_EXHAUSTED", http.StatusTooManyRequests)
	err := renderer.fail(upstreamErr)
	require.Error(t, err)
	_, isFailover := err.(*UpstreamFailoverError)
	require.False(t, isFailover, "a committed stream cannot switch accounts")

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"upstream_error"`)
	require.Contains(t, body, `"code":"NATIVE_QUOTA_EXHAUSTED"`)
}
