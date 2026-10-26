package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func compositeCandidateRoutes(routes ...CompositeModelRoute) compositeRouteRepoStub {
	for i := range routes {
		routes[i].GroupID = 7
		routes[i].Enabled = true
		if routes[i].Endpoint == "" {
			routes[i].Endpoint = CompositeRouteEndpointAny
		}
	}
	return compositeRouteRepoStub{routes: routes}
}

// 使用者情境：同一個 public model 依 priority 串起 opencode_go → command_code
// → deepseek，同一個 client 請求可依序嘗試。
func TestCompositeRouteCandidatesOrderByPriority(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 30, PublicModel: "deepseek-v4.1-flash", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, Priority: 30},
		CompositeModelRoute{ID: 10, PublicModel: "deepseek-v4.1-flash", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "deepseek-v4.1-flash", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformCommandCode, Priority: 20},
		CompositeModelRoute{ID: 40, PublicModel: "deepseek-v4.1-flash", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformDeepseek, Priority: 40},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "deepseek-v4.1-flash", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, PlatformOpenCodeGo, decision.TargetPlatform)
	require.Equal(t, int64(10), decision.Route.ID)
	// Route 30 targets the same platform as route 10, so it is dropped: the
	// account-level failover already covers that pool.
	require.Equal(t, []CompositeRouteCandidate{
		{TargetPlatform: PlatformOpenCodeGo, UpstreamModel: "deepseek-v4.1-flash", Priority: 10, RouteID: 10, Endpoint: CompositeRouteEndpointAny},
		{TargetPlatform: PlatformCommandCode, UpstreamModel: "deepseek-v4.1-flash", Priority: 20, RouteID: 20, Endpoint: CompositeRouteEndpointAny},
		{TargetPlatform: PlatformDeepseek, UpstreamModel: "deepseek-v4.1-flash", Priority: 40, RouteID: 40, Endpoint: CompositeRouteEndpointAny},
	}, decision.Candidates)
}

// 單一路由不得產生候選鏈，既有行為完全不受影響。
func TestCompositeRouteCandidatesEmptyForSingleRoute(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, Priority: 10},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, PlatformOpenAI, decision.TargetPlatform)
	require.Empty(t, decision.Candidates)
}

// exact 與 prefix 是不同請求形狀，不得混成同一條鏈。
func TestCompositeRouteCandidatesDoNotMixMatchTypes(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "claude-sonnet-4", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "claude-", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformDeepseek, Priority: 20},
		CompositeModelRoute{ID: 30, PublicModel: "claude-sonnet-4", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformCommandCode, Priority: 30},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "claude-sonnet-4", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, int64(10), decision.Route.ID)
	require.Len(t, decision.Candidates, 2)
	require.Equal(t, int64(10), decision.Candidates[0].RouteID)
	require.Equal(t, int64(30), decision.Candidates[1].RouteID)
	for _, candidate := range decision.Candidates {
		require.NotEqual(t, PlatformDeepseek, candidate.TargetPlatform, "prefix route must not join an exact chain")
	}
}

// 同一平台多列只留 priority 最小的一條。
func TestCompositeRouteCandidatesDedupeByPlatform(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, UpstreamModel: "go-a", Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, UpstreamModel: "go-b", Priority: 20},
		CompositeModelRoute{ID: 30, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformDeepseek, UpstreamModel: "deepseek-chat", Priority: 30},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointChatCompletions)

	require.NoError(t, err)
	require.Equal(t, []CompositeRouteCandidate{
		{TargetPlatform: PlatformOpenCodeGo, UpstreamModel: "go-a", Priority: 10, RouteID: 10, Endpoint: CompositeRouteEndpointAny},
		{TargetPlatform: PlatformDeepseek, UpstreamModel: "deepseek-chat", Priority: 30, RouteID: 30, Endpoint: CompositeRouteEndpointAny},
	}, decision.Candidates)
}

// endpoint 不相容的路由不得進入候選鏈；any 相容所有 endpoint。
func TestCompositeRouteCandidatesFilterByEndpoint(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, Endpoint: CompositeRouteEndpointResponses, Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformDeepseek, Endpoint: CompositeRouteEndpointChatCompletions, Priority: 20},
		CompositeModelRoute{ID: 30, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformCommandCode, Endpoint: CompositeRouteEndpointAny, Priority: 30},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.Equal(t, int64(10), decision.Route.ID)
	require.Equal(t, []CompositeRouteCandidate{
		{TargetPlatform: PlatformOpenAI, UpstreamModel: "alias", Priority: 10, RouteID: 10, Endpoint: CompositeRouteEndpointResponses},
		{TargetPlatform: PlatformCommandCode, UpstreamModel: "alias", Priority: 30, RouteID: 30, Endpoint: CompositeRouteEndpointAny},
	}, decision.Candidates)
}

// 候選鏈以命中路由開頭，即使另一條 any-endpoint 路由排序更前面。
func TestCompositeRouteCandidatesLeaderMatchesSelectedRoute(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		// any-endpoint + 更低 priority：排序會贏，但 endpoint 權重輸給下面那條。
		CompositeModelRoute{ID: 10, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformDeepseek, Endpoint: CompositeRouteEndpointAny, Priority: 1},
		CompositeModelRoute{ID: 20, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenAI, Endpoint: CompositeRouteEndpointResponses, Priority: 100},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.Equal(t, int64(20), decision.Route.ID)
	require.Equal(t, decision.TargetPlatform, decision.Candidates[0].TargetPlatform)
	require.Equal(t, int64(20), decision.Candidates[0].RouteID)
	require.Equal(t, int64(10), decision.Candidates[1].RouteID)
}

// prefix 路由重寫：upstream_model 留空時沿用具體請求模型；顯式填寫則固定。
func TestCompositeRouteCandidatesResolveUpstreamModels(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "deepseek-v4", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformOpenCodeGo, Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "deepseek-v4", MatchType: CompositeRouteMatchPrefix, TargetPlatform: PlatformDeepseek, UpstreamModel: "deepseek-chat", Priority: 20},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "deepseek-v4-flash", CompositeRouteEndpointChatCompletions)

	require.NoError(t, err)
	require.Len(t, decision.Candidates, 2)
	require.Equal(t, "deepseek-v4-flash", decision.Candidates[0].UpstreamModel)
	require.Equal(t, "deepseek-chat", decision.Candidates[1].UpstreamModel)
}

// 沒有 explicit route 時（detector / account ownership）不得產生候選鏈。
func TestCompositeRouteCandidatesAbsentWithoutExplicitRoute(t *testing.T) {
	resolver := NewCompositeRouteResolver(nil)

	decision, err := resolver.Resolve(context.Background(), 7, "gpt-5", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, CompositeRouteSourceDetector, decision.Source)
	require.Empty(t, decision.Candidates)
}

// 停用的路由不得進入候選鏈。
func TestCompositeRouteCandidatesIgnoreDisabledRoutes(t *testing.T) {
	routes := []CompositeModelRoute{
		{ID: 10, GroupID: 7, Enabled: true, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, Priority: 10},
		{ID: 20, GroupID: 7, Enabled: false, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformDeepseek, Priority: 20},
	}
	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: routes})

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.Empty(t, decision.Candidates)
}

// 未登記平台（例如打錯的 target_platform）不得進入候選鏈。
func TestCompositeRouteCandidatesSkipUnknownPlatforms(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeCandidateRoutes(
		CompositeModelRoute{ID: 10, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformOpenCodeGo, Priority: 10},
		CompositeModelRoute{ID: 20, PublicModel: "alias", MatchType: CompositeRouteMatchExact, TargetPlatform: PlatformComposite, Priority: 20},
	))

	decision, err := resolver.Resolve(context.Background(), 7, "alias", CompositeRouteEndpointResponses)

	require.NoError(t, err)
	require.Empty(t, decision.Candidates)
}
