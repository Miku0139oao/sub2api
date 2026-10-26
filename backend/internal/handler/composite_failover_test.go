package handler

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newCompositeFailoverTestContext(t *testing.T, decision service.CompositeRouteDecision) (*gin.Context, *service.APIKey) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	if decision.Matched {
		c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
	}
	apiKey := &service.APIKey{
		GroupID: int64Ptr(7),
		Group:   &service.Group{ID: 7, Platform: service.PlatformComposite},
	}
	return c, apiKey
}

func int64Ptr(v int64) *int64 { return &v }

func compositeChainDecision() service.CompositeRouteDecision {
	candidates := []service.CompositeRouteCandidate{
		{TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "model-a", Priority: 10, RouteID: 10, Endpoint: service.CompositeRouteEndpointAny},
		{TargetPlatform: service.PlatformCommandCode, UpstreamModel: "model-b", Priority: 20, RouteID: 20, Endpoint: service.CompositeRouteEndpointAny},
		{TargetPlatform: service.PlatformDeepseek, UpstreamModel: "model-c", Priority: 30, RouteID: 30, Endpoint: service.CompositeRouteEndpointAny},
	}
	return service.CompositeRouteDecision{
		Matched:        true,
		Source:         service.CompositeRouteSourceExplicit,
		GroupID:        7,
		PublicModel:    "deepseek-v4.1-flash",
		TargetPlatform: candidates[0].TargetPlatform,
		UpstreamModel:  candidates[0].UpstreamModel,
		Endpoint:       service.CompositeRouteEndpointResponses,
		Candidates:     candidates,
	}
}

// 使用者情境：第一個平台耗盡後切到第二個，再切到第三個，最後耗盡。
func TestAdvanceCompositeCandidateWalksChainInOrder(t *testing.T) {
	c, apiKey := newCompositeFailoverTestContext(t, compositeChainDecision())

	first, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, ok)
	// opencode_go 是首選，已由 decision 寫入 context，因此第一個切換目標是
	// command_code（opencode_go 不重試）。
	require.Equal(t, service.PlatformCommandCode, first.TargetPlatform)
	require.Equal(t, "model-b", first.UpstreamModel)
	platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	require.Equal(t, service.PlatformCommandCode, platform)
	model, _ := service.ResolvedUpstreamModelFromContext(c.Request.Context())
	require.Equal(t, "model-b", model)

	second, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, ok)
	require.Equal(t, service.PlatformDeepseek, second.TargetPlatform)
	require.Equal(t, []string{service.PlatformOpenCodeGo, service.PlatformCommandCode, service.PlatformDeepseek}, service.CompositeAttemptsFromContext(c.Request.Context()))

	_, ok = advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, ok, "chain must be exhausted after the last platform")
}

// 串流已開始時不得切換平台。
func TestAdvanceCompositeCandidateRefusesAfterStreamStarted(t *testing.T) {
	c, apiKey := newCompositeFailoverTestContext(t, compositeChainDecision())

	_, ok := advanceCompositeCandidate(c, apiKey, true, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, ok)
	platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	require.Equal(t, service.PlatformOpenCodeGo, platform)
}

// 沒有候選鏈（單一路由 / detector 解析）時完全不啟用。
func TestAdvanceCompositeCandidateNoopWithoutChain(t *testing.T) {
	decision := compositeChainDecision()
	decision.Candidates = nil
	c, apiKey := newCompositeFailoverTestContext(t, decision)

	_, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, ok)
	platform, _ := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	require.Equal(t, service.PlatformOpenCodeGo, platform)
}

// 非 composite 分組不得切換。
func TestAdvanceCompositeCandidateNoopForConcreteGroup(t *testing.T) {
	c, _ := newCompositeFailoverTestContext(t, compositeChainDecision())
	apiKey := &service.APIKey{GroupID: int64Ptr(7), Group: &service.Group{ID: 7, Platform: service.PlatformOpenAI}}

	_, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, ok)
}

// 只在同一 gateway 家族內切換：OpenAI 網關族的 handler 不得把請求切到
// Anthropic 平台（請求與回應格式不同，無法在請求中途轉換）。
func TestAdvanceCompositeCandidateStaysWithinGatewayFamily(t *testing.T) {
	decision := compositeChainDecision()
	decision.Candidates = []service.CompositeRouteCandidate{
		{TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "model-a", Priority: 10, RouteID: 10},
		{TargetPlatform: service.PlatformAnthropic, UpstreamModel: "claude-sonnet-4", Priority: 20, RouteID: 20},
		{TargetPlatform: service.PlatformDeepseek, UpstreamModel: "model-c", Priority: 30, RouteID: 30},
	}
	c, apiKey := newCompositeFailoverTestContext(t, decision)

	candidate, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, ok)
	require.Equal(t, service.PlatformDeepseek, candidate.TargetPlatform, "Anthropic candidate must be skipped from an OpenAI-family handler")
}

// 只有在 OpenAI 網關族的 handler 才會啟用；Anthropic 網關族同理。
func TestAdvanceCompositeCandidateAnthropicFamilySkipsOpenAI(t *testing.T) {
	decision := compositeChainDecision()
	decision.TargetPlatform = service.PlatformAnthropic
	decision.UpstreamModel = "claude-sonnet-4"
	decision.Candidates = []service.CompositeRouteCandidate{
		{TargetPlatform: service.PlatformAnthropic, UpstreamModel: "claude-sonnet-4", Priority: 10, RouteID: 10},
		{TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "model-a", Priority: 20, RouteID: 20},
		{TargetPlatform: service.PlatformTypeSafe, UpstreamModel: "claude-sonnet-4", Priority: 30, RouteID: 30},
	}
	c, apiKey := newCompositeFailoverTestContext(t, decision)

	candidate, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, ok)
	require.Equal(t, service.PlatformTypeSafe, candidate.TargetPlatform)
}

// 候選平台都是其他 gateway 家族時，鏈等同耗盡。
func TestAdvanceCompositeCandidateExhaustedWhenFamiliesDiffer(t *testing.T) {
	decision := compositeChainDecision()
	decision.Candidates = []service.CompositeRouteCandidate{
		{TargetPlatform: service.PlatformOpenCodeGo, UpstreamModel: "model-a", Priority: 10, RouteID: 10},
		{TargetPlatform: service.PlatformAnthropic, UpstreamModel: "claude-sonnet-4", Priority: 20, RouteID: 20},
	}
	c, apiKey := newCompositeFailoverTestContext(t, decision)

	_, ok := advanceCompositeCandidate(c, apiKey, false, service.OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.False(t, ok)
}

func TestCompositeGatewayFamilyGroupsPlatforms(t *testing.T) {
	for _, platform := range []string{
		service.PlatformOpenCodeGo,
		service.PlatformCommandCode,
		service.PlatformDeepseek,
	} {
		family, ok := compositeGatewayFamily(platform)
		require.True(t, ok, platform)
		require.Equal(t, string(domain.PlatformGatewayOpenAI), string(family), platform)
	}
	openAI, _ := compositeGatewayFamily(service.PlatformCommandCode)
	deepseek, _ := compositeGatewayFamily(service.PlatformDeepseek)
	require.Equal(t, openAI, deepseek, "opencode_go/command_code/deepseek must share the OpenAI gateway")

	anthropic, _ := compositeGatewayFamily(service.PlatformAnthropic)
	require.NotEqual(t, openAI, anthropic)
}

func TestRewriteCompositeAttemptBodyAppliesRouteThenChannelMapping(t *testing.T) {
	original := []byte(`{"model":"public-alias","messages":[]}`)
	replace := func(body []byte, model string) []byte {
		return service.ReplaceModelInBody(body, model)
	}
	candidate := service.CompositeRouteCandidate{UpstreamModel: "route-model"}

	routeOnly := rewriteCompositeAttemptBody(original, service.ChannelMappingResult{MappedModel: "public-alias"}, candidate, replace)
	require.Contains(t, string(routeOnly), `"route-model"`)

	channelWins := rewriteCompositeAttemptBody(original, service.ChannelMappingResult{Mapped: true, MappedModel: "channel-model"}, candidate, replace)
	require.Contains(t, string(channelWins), `"channel-model"`)
	require.NotContains(t, string(channelWins), `"route-model"`)

	// 空 upstream_model 保留原始 body。
	unchanged := rewriteCompositeAttemptBody(original, service.ChannelMappingResult{MappedModel: "public-alias"}, service.CompositeRouteCandidate{}, replace)
	require.Equal(t, original, unchanged)
}

func TestShouldAdvanceCompositeOnError(t *testing.T) {
	require.True(t, shouldAdvanceCompositeOnError(nil), "no upstream error (empty pool) may try the next platform")

	requestScoped := &service.UpstreamFailoverError{
		StatusCode:        400,
		Stage:             service.GatewayFailureStageInference,
		Scope:             service.GatewayFailureScopeRequest,
		NextAccountAction: service.NextAccountStop,
	}
	require.False(t, shouldAdvanceCompositeOnError(requestScoped), "request errors must not burn the chain")

	switchable := &service.UpstreamFailoverError{
		StatusCode:        503,
		Stage:             service.GatewayFailureStageInference,
		Scope:             service.GatewayFailureScopeProvider,
		NextAccountAction: service.NextAccountRetry,
	}
	require.True(t, shouldAdvanceCompositeOnError(switchable))
}

func TestShouldAdvanceCompositeOnSelectionError(t *testing.T) {
	require.True(t, shouldAdvanceCompositeOnSelectionError(errors.New("no available accounts"), false),
		"an empty platform pool is exactly what the next platform is for")
	require.True(t, shouldAdvanceCompositeOnSelectionError(errors.New("no available accounts"), true),
		"non-compact endpoints do not care about the compact verdict")
	require.False(t, shouldAdvanceCompositeOnSelectionError(service.ErrNoAvailableCompactAccounts, true),
		"compact support is a per-platform capability with a dedicated client contract")
}
