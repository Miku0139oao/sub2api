package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func lbCandidates(weights map[string]int, order ...string) []CompositeRouteCandidate {
	out := make([]CompositeRouteCandidate, 0, len(order))
	for i, platform := range order {
		out = append(out, CompositeRouteCandidate{
			TargetPlatform: platform,
			UpstreamModel:  "m-" + platform,
			Priority:       (i + 1) * 10,
			RouteID:        int64(i + 1),
			Weight:         weights[platform],
		})
	}
	return out
}

// 沒有 weight（全部 0）必須完全保留 priority 順序，等同改動前行為。
func TestCompositeLBWithoutWeightsKeepsPriorityOrder(t *testing.T) {
	candidates := lbCandidates(nil, PlatformOpenCodeGo, PlatformCommandCode, PlatformDeepseek)

	for i := 0; i < 100; i++ {
		ordered := orderCompositeCandidatesForLoadBalancing(candidates, fmt.Sprintf("session-%d", i), "model")
		require.Equal(t, PlatformOpenCodeGo, ordered[0].TargetPlatform,
			"weight=0 must never move a session off the highest-priority platform")
	}
}

// 沒有 session hash 時不啟用分流。
func TestCompositeLBWithoutSessionHashKeepsPriorityOrder(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  1,
		PlatformCommandCode: 9,
	}, PlatformOpenCodeGo, PlatformCommandCode)

	for _, hash := range []string{"", "   "} {
		ordered := orderCompositeCandidatesForLoadBalancing(candidates, hash, "model")
		require.Equal(t, PlatformOpenCodeGo, ordered[0].TargetPlatform)
	}
}

// 同一 session 必須永遠落在同一平台（cache / previous_response_id 的前提）。
func TestCompositeLBSameSessionStaysOnSamePlatform(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  50,
		PlatformCommandCode: 50,
	}, PlatformOpenCodeGo, PlatformCommandCode)

	for i := 0; i < 50; i++ {
		hash := fmt.Sprintf("stable-session-%d", i)
		first := orderCompositeCandidatesForLoadBalancing(candidates, hash, "model")[0].TargetPlatform
		for j := 0; j < 5; j++ {
			again := orderCompositeCandidatesForLoadBalancing(candidates, hash, "model")[0].TargetPlatform
			require.Equal(t, first, again, "session %s moved between platforms", hash)
		}
	}
}

// 權重比例要接近設定值。
func TestCompositeLBHonorsWeights(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  80,
		PlatformCommandCode: 20,
	}, PlatformOpenCodeGo, PlatformCommandCode)

	const sessions = 20000
	counts := map[string]int{}
	for i := 0; i < sessions; i++ {
		primary := orderCompositeCandidatesForLoadBalancing(candidates, fmt.Sprintf("s-%d", i), "model")[0].TargetPlatform
		counts[primary]++
	}

	share := float64(counts[PlatformOpenCodeGo]) / float64(sessions)
	require.InDelta(t, 0.80, share, 0.02,
		"80/20 split expected, got opencode=%d command=%d", counts[PlatformOpenCodeGo], counts[PlatformCommandCode])
}

// 三個平台權重 1:1:2，驗證多平台時比例仍正確。
func TestCompositeLBHonorsWeightsThreePlatforms(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  1,
		PlatformCommandCode: 1,
		PlatformDeepseek:    2,
	}, PlatformOpenCodeGo, PlatformCommandCode, PlatformDeepseek)

	const sessions = 30000
	counts := map[string]int{}
	for i := 0; i < sessions; i++ {
		counts[orderCompositeCandidatesForLoadBalancing(candidates, fmt.Sprintf("s-%d", i), "model")[0].TargetPlatform]++
	}

	for platform, want := range map[string]float64{
		PlatformOpenCodeGo:  0.25,
		PlatformCommandCode: 0.25,
		PlatformDeepseek:    0.50,
	} {
		share := float64(counts[platform]) / float64(sessions)
		require.InDelta(t, want, share, 0.03, "%s got share %.3f, want %.3f", platform, share, want)
	}
}

// weight=0 的平台永遠不會被選為首選，但仍在鏈中當 failover 備援。
func TestCompositeLBZeroWeightPlatformIsFailoverOnly(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  1,
		PlatformCommandCode: 0,
	}, PlatformOpenCodeGo, PlatformCommandCode)

	for i := 0; i < 500; i++ {
		ordered := orderCompositeCandidatesForLoadBalancing(candidates, fmt.Sprintf("s-%d", i), "model")
		require.Equal(t, PlatformOpenCodeGo, ordered[0].TargetPlatform,
			"weight=0 platform must never be primary")
		require.Equal(t, PlatformCommandCode, ordered[1].TargetPlatform,
			"weight=0 platform must remain in the chain as a failover target")
	}
}

// rendezvous 的核心性質：調整一個平台的權重，只會影響原本屬於它的 session。
func TestCompositeLBRendezvousMinimizesRemapping(t *testing.T) {
	before := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  50,
		PlatformCommandCode: 50,
	}, PlatformOpenCodeGo, PlatformCommandCode)
	after := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  50,
		PlatformCommandCode: 50,
		PlatformDeepseek:    50, // 新增第三個平台
	}, PlatformOpenCodeGo, PlatformCommandCode, PlatformDeepseek)

	const sessions = 5000
	moved := 0
	for i := 0; i < sessions; i++ {
		hash := fmt.Sprintf("s-%d", i)
		a := orderCompositeCandidatesForLoadBalancing(before, hash, "model")[0].TargetPlatform
		b := orderCompositeCandidatesForLoadBalancing(after, hash, "model")[0].TargetPlatform
		if a != b {
			moved++
		}
	}
	// 新平台吃掉約 1/3，舊 session 應該只有約 1/3 被移走；若用 hash%total 會
	// 幾乎全部重排。這裡放寬到 45% 以吸收統計波動。
	share := float64(moved) / float64(sessions)
	require.Less(t, share, 0.45, "adding a platform remapped %.1f%% of sessions", share*100)
}

// 不同 model 應該有各自的分布（避免同一 session 換 model 時分到同一平台）。
func TestCompositeLBScopesByModel(t *testing.T) {
	candidates := lbCandidates(map[string]int{
		PlatformOpenCodeGo:  50,
		PlatformCommandCode: 50,
	}, PlatformOpenCodeGo, PlatformCommandCode)

	differs := 0
	for i := 0; i < 200; i++ {
		hash := fmt.Sprintf("s-%d", i)
		a := orderCompositeCandidatesForLoadBalancing(candidates, hash, "model-a")[0].TargetPlatform
		b := orderCompositeCandidatesForLoadBalancing(candidates, hash, "model-b")[0].TargetPlatform
		if a != b {
			differs++
		}
	}
	require.Greater(t, differs, 40, "model should participate in the hash")
}

// Resolver 整合：weight 讓 decision 的首選平台跟著變，並標記 LoadBalanced。
func TestCompositeResolveAppliesLoadBalancing(t *testing.T) {
	routes := []CompositeModelRoute{
		{ID: 1, GroupID: 7, Enabled: true, PublicModel: "alias", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformOpenCodeGo, Priority: 10, Weight: 1},
		{ID: 2, GroupID: 7, Enabled: true, PublicModel: "alias", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformCommandCode, Priority: 20, Weight: 1},
	}
	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: routes})

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		ctx := WithCompositeLBSessionHash(context.Background(), fmt.Sprintf("session-%d", i))
		decision, err := resolver.Resolve(ctx, 7, "alias", CompositeRouteEndpointResponses)
		require.NoError(t, err)
		require.True(t, decision.Matched)
		require.Len(t, decision.Candidates, 2)
		// 首選平台必須與候選鏈一致
		require.Equal(t, decision.Candidates[0].TargetPlatform, decision.TargetPlatform)
		require.Equal(t, decision.Candidates[0].UpstreamModel, decision.UpstreamModel)
		seen[decision.TargetPlatform] = true
	}
	require.True(t, seen[PlatformOpenCodeGo] && seen[PlatformCommandCode],
		"both weighted platforms should receive sessions")
}

// 沒有 weight 時 Resolve 仍是原本的 priority 首選，且不標記 LoadBalanced。
func TestCompositeResolveWithoutWeightIsUnchanged(t *testing.T) {
	routes := []CompositeModelRoute{
		{ID: 1, GroupID: 7, Enabled: true, PublicModel: "alias", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformOpenCodeGo, Priority: 10},
		{ID: 2, GroupID: 7, Enabled: true, PublicModel: "alias", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformCommandCode, Priority: 20},
	}
	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: routes})

	ctx := WithCompositeLBSessionHash(context.Background(), "any-session")
	decision, err := resolver.Resolve(ctx, 7, "alias", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.Equal(t, PlatformOpenCodeGo, decision.TargetPlatform)
	require.False(t, decision.LoadBalanced)
}

func TestCompositeLBSessionSeed(t *testing.T) {
	require.Equal(t, "sess-header", CompositeLBSessionSeed(
		map[string]string{"session-id": "sess-header"}, nil))

	require.Equal(t, "cache-key", CompositeLBSessionSeed(nil,
		[]byte(`{"model":"m","prompt_cache_key":"cache-key"}`)))

	require.Equal(t, "live-session", CompositeLBSessionSeed(nil,
		[]byte(`{"session":{"session_id":"live-session"}}`)))

	require.Equal(t, "", CompositeLBSessionSeed(nil, []byte(`{"model":"m"}`)),
		"no session identity means no load balancing rather than a random spread")

	// metadata.user_id 有 session_id 時取它
	metadata := `{"metadata":{"user_id":"user_abc_account__session_123e4567-e89b-12d3-a456-426614174000"}}`
	require.NotEmpty(t, CompositeLBSessionSeed(nil, []byte(metadata)))
}
