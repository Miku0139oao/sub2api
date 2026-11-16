//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func uint64Ptr(value uint64) *uint64 { return &value }

func geminiAppTestTurn(emit func(geminiapp.ToolEvent) error) *geminiAppTurn {
	policy, _ := geminiapp.CompileToolPolicy(nil, nil)
	return newGeminiAppTurn(geminiapp.ModelArgon, geminiapp.ModeUltra, geminiapp.ThinkingExtended, policy, emit)
}

func geminiAppTextInfo(text, id string) *geminiapp.GenerateInfo {
	return &geminiapp.GenerateInfo{Text: text, HasText: true, ResponseID: id}
}

func TestGeminiAppTurnAccumulatesSnapshots(t *testing.T) {
	var emitted []geminiapp.ToolEvent
	turn := geminiAppTestTurn(func(event geminiapp.ToolEvent) error {
		emitted = append(emitted, event)
		return nil
	})

	require.NoError(t, turn.ingest(geminiAppTextInfo("Hello", "resp-1")))
	require.Empty(t, emitted, "text must be withheld until the resolved mode is verified")

	require.NoError(t, turn.ingest(&geminiapp.GenerateInfo{ResponseID: "resp-1", ResolvedModeCategory: uint64Ptr(geminiapp.ModeUltra)}))
	require.NotEmpty(t, emitted)

	require.NoError(t, turn.ingest(geminiAppTextInfo("Hello world", "resp-1")))
	require.NoError(t, turn.ingest(&geminiapp.GenerateInfo{ResponseID: "resp-1", Completed: true}))

	result, err := turn.finalize()
	require.NoError(t, err)
	require.Equal(t, "Hello world", result.Content)
	require.Equal(t, "resp-1", result.Response)
	require.Equal(t, uint64(geminiapp.ModeUltra), *result.Mode)
	require.Equal(t, 4, result.Frames)
}

func TestGeminiAppTurnRejectsNonMonotonicSnapshotsAfterPublish(t *testing.T) {
	turn := geminiAppTestTurn(func(geminiapp.ToolEvent) error { return nil })
	require.NoError(t, turn.ingest(&geminiapp.GenerateInfo{ResponseID: "resp-1", ResolvedModeCategory: uint64Ptr(geminiapp.ModeUltra)}))
	require.NoError(t, turn.ingest(geminiAppTextInfo("first", "resp-1")))

	err := turn.ingest(geminiAppTextInfo("second", "resp-1"))
	require.Error(t, err)
	appErr, ok := geminiapp.AsError(err)
	require.True(t, ok)
	require.Equal(t, "NON_MONOTONIC_NATIVE_RESPONSE", appErr.Code)
}

func TestGeminiAppTurnRejectsResponseIDChange(t *testing.T) {
	turn := geminiAppTestTurn(func(geminiapp.ToolEvent) error { return nil })
	require.NoError(t, turn.ingest(geminiAppTextInfo("a", "resp-1")))
	err := turn.ingest(geminiAppTextInfo("ab", "resp-2"))
	require.Error(t, err)
}

func TestGeminiAppTurnQuotaDowngradeIsRetryable(t *testing.T) {
	turn := geminiAppTestTurn(func(geminiapp.ToolEvent) error { return nil })
	turn.fallback = true
	turn.downgradeReason = uint64Ptr(2)

	err := turn.ingest(&geminiapp.GenerateInfo{})
	require.Error(t, err)
	appErr, ok := geminiapp.AsError(err)
	require.True(t, ok)
	require.Equal(t, "NATIVE_QUOTA_EXHAUSTED", appErr.Code)
	require.Equal(t, http.StatusTooManyRequests, appErr.Status)

	failover := geminiAppFailoverError(err)
	require.NotNil(t, failover)
	require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
}

func TestGeminiAppTurnRequiresCompletionAndModeEvidence(t *testing.T) {
	turn := geminiAppTestTurn(func(geminiapp.ToolEvent) error { return nil })
	require.NoError(t, turn.ingest(&geminiapp.GenerateInfo{ResponseID: "r", ResolvedModeCategory: uint64Ptr(geminiapp.ModeUltra)}))
	require.NoError(t, turn.ingest(geminiAppTextInfo("done", "r")))
	_, err := turn.finalize()
	require.Error(t, err)
	appErr, _ := geminiapp.AsError(err)
	require.Equal(t, "INCOMPLETE_NATIVE_RESPONSE", appErr.Code)

	turn2 := geminiAppTestTurn(func(geminiapp.ToolEvent) error { return nil })
	require.NoError(t, turn2.ingest(geminiAppTextInfo("done", "r")))
	require.NoError(t, turn2.ingest(&geminiapp.GenerateInfo{ResponseID: "r", Completed: true}))
	_, err = turn2.finalize()
	require.Error(t, err)
	appErr, _ = geminiapp.AsError(err)
	require.Equal(t, "MISSING_NATIVE_MODE_EVIDENCE", appErr.Code)
}

func TestGeminiAppEstimateUsageCountsPromptAndCalls(t *testing.T) {
	result := &geminiAppTurnResult{
		Content: "hello world",
		Calls: []geminiapp.ToolCall{{
			ID: "call_1",
			Function: geminiapp.ToolFunction{
				Name:      "lookup",
				Arguments: `{"q":"weather"}`,
			},
		}},
	}
	usage := geminiAppEstimateUsage("prompt text that is long enough", result)
	require.Greater(t, usage.InputTokens, 0)
	require.Greater(t, usage.OutputTokens, 0)
}

func TestGeminiAppChatResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	policy, _ := geminiapp.CompileToolPolicy(nil, nil)
	renderer := newGeminiAppRenderer(geminiAppProtocolChatCompletions, c, &geminiapp.CanonicalRequest{
		Model:         geminiapp.ModelArgon,
		ThinkingLevel: geminiapp.ThinkingExtended,
		Messages:      []geminiapp.ChatMessage{{Role: "user", Content: "hi"}},
		Policy:        policy,
	}, time.Unix(0, 0), "chatcmpl-test")
	result := &geminiAppTurnResult{Content: "hello"}
	forwardResult, err := renderer.finish(result, "prompt")
	require.NoError(t, err)
	require.NotNil(t, forwardResult)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "chat.completion", payload["object"])
	require.Equal(t, "gemini-argon", payload["model"])
	choices := payload["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	require.Equal(t, "hello", message["content"])
}

func TestGeminiAppResponsesResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	renderer := newGeminiAppRenderer(geminiAppProtocolResponses, c, &geminiapp.CanonicalRequest{
		Model: geminiapp.ModelHydrogen,
	}, time.Unix(0, 0), "resp_test")
	result := &geminiAppTurnResult{
		Content: "hello",
		Calls: []geminiapp.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: geminiapp.ToolFunction{Name: "lookup", Arguments: `{"q":"x"}`},
		}},
	}
	_, err := renderer.finish(result, "prompt")
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "response", payload["object"])
	outputs := payload["output"].([]any)
	require.Len(t, outputs, 2)
	require.Equal(t, "message", outputs[0].(map[string]any)["type"])
	require.Equal(t, "function_call", outputs[1].(map[string]any)["type"])
	require.Equal(t, "call_1", outputs[1].(map[string]any)["call_id"])
}

func TestParseGeminiAppOAuthProfileAcceptsSnakeCase(t *testing.T) {
	raw := `{"context":2,"enable_fst":true,"client_id":"123456789012-abc123def.apps.googleusercontent.com","client_secret":"secret","scopes":["openid","email"],"redirect_uri":"http://localhost:18765"}`
	profile, err := parseGeminiAppOAuthProfile(raw)
	require.NoError(t, err)
	require.Equal(t, "123456789012-abc123def.apps.googleusercontent.com", profile.ClientID)
	require.Equal(t, "http://localhost:18765", profile.RedirectURI)
}

func TestParseGeminiAppOAuthProfileRejectsInvalidClientID(t *testing.T) {
	raw := `{"context":2,"enableFst":true,"clientId":"not-a-google-client","clientSecret":"secret"}`
	_, err := parseGeminiAppOAuthProfile(raw)
	require.Error(t, err)
}

func TestGeminiAppCapabilitiesReportsMissingProfile(t *testing.T) {
	svc := NewGeminiAppOAuthService(nil)
	svc.SetProfileLoader(func() (*geminiapp.OAuthProfile, error) {
		return nil, geminiapp.NewError("OAUTH_PROFILE_REQUIRED", 409)
	})
	require.False(t, svc.Capabilities().Configured)
}

func TestGeminiAppMappedErrorAppliesPassthroughRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{
		newNonFailoverPassthroughRule(http.StatusTooManyRequests, "quota", http.StatusTeapot, "自定义配额错误"),
	})
	BindErrorPassthroughService(c, ruleSvc)

	err := writeGeminiAppMappedError(c, geminiAppProtocolChatCompletions, geminiapp.NewError("NATIVE_QUOTA_EXHAUSTED", http.StatusTooManyRequests))
	require.Error(t, err)
	require.Equal(t, http.StatusTeapot, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upstream_error", errField["type"])
	require.Equal(t, "自定义配额错误", errField["message"])
}

func TestGeminiAppMappedErrorKeepsDefaultWithoutRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	err := writeGeminiAppMappedError(c, geminiAppProtocolChatCompletions, geminiapp.NewError("UNSUPPORTED_MODEL", http.StatusBadRequest))
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "invalid_request_error", errField["type"])
	require.Contains(t, errField["message"], "gemini-argon")
}
