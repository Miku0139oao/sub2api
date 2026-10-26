//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// OpenAI 渠道按次定价只配置按上下文分档（无尺寸档）时，尺寸档未命中必须用 token
// 上下文匹配档位；漏传 Tokens 会让档位永不命中而按 $0 结算。
func TestCalculateOpenAIImageCost_ChannelPerRequestContextTierUsesTokens(t *testing.T) {
	groupID := int64(911)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	cached, ok := resolver.channelService.cache.Load().(*channelCache)
	require.True(t, ok)
	maxTokens := 1000
	cached.pricingByGroupModel[channelModelKey{groupID: groupID, model: model}] = &ChannelModelPricing{
		BillingMode: BillingModePerRequest,
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, PerRequestPrice: f64p(0.1)},
			{MinTokens: 1000, PerRequestPrice: f64p(0.3)},
		},
	}
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
	}
	apiKey := &APIKey{ID: 9, GroupID: &groupID, Group: group}
	svc := &OpenAIGatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateOpenAIImageCost(context.Background(), model, apiKey, &OpenAIForwardResult{
		Model: model, ImageCount: 2, ImageSize: "1K", ImageEndpoint: true,
		Usage: OpenAIUsage{InputTokens: 5000, OutputTokens: 100},
	}, 1)

	require.NotNil(t, cost)
	require.InDelta(t, 0.6, cost.TotalCost, 1e-12, "应按上下文档 0.3 × 2 张结算")
}

// 多尺寸响应必须逐档计价：1×1K + 1×4K 不能整体按 4K 单价结算。
func TestCalculateRecordUsageCost_MixedImageSizesBilledPerTier(t *testing.T) {
	groupID := int64(912)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.1),
		ImagePrice4K:         f64p(0.4),
	}
	apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: model, ImageCount: 2, ImageSize: "4K", ImageEndpoint: true,
		ImageSizeBreakdown: map[string]int{ImageBillingSize1K: 1, ImageBillingSize4K: 1},
	}, apiKey, model, 1, 1, time.Time{})

	require.NotNil(t, cost)
	require.InDelta(t, 0.5, cost.TotalCost, 1e-12, "1×0.1 + 1×0.4")
}

// 分档数据与总张数对不上时保持原来的单档行为，避免因脏数据少收。
func TestCalculateRecordUsageCost_IncompleteBreakdownFallsBackToSingleTier(t *testing.T) {
	groupID := int64(913)
	model := "gpt-image-2"
	resolver := newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
		ImagePrice1K:         f64p(0.1),
		ImagePrice4K:         f64p(0.4),
	}
	apiKey := &APIKey{ID: 11, GroupID: &groupID, Group: group}
	svc := &GatewayService{billingService: resolver.billingService, resolver: resolver}

	cost := svc.calculateRecordUsageCost(context.Background(), &ForwardResult{
		Model: model, ImageCount: 3, ImageSize: "4K", ImageEndpoint: true,
		ImageSizeBreakdown: map[string]int{ImageBillingSize1K: 1, ImageBillingSize4K: 1},
	}, apiKey, model, 1, 1, time.Time{})

	require.NotNil(t, cost)
	require.InDelta(t, 1.2, cost.TotalCost, 1e-12, "不完整分档按 3×0.4 单档结算")
}

func TestImageBillingTierCounts(t *testing.T) {
	require.Nil(t, imageBillingTierCounts(nil, 2))
	require.Nil(t, imageBillingTierCounts(map[string]int{ImageBillingSize4K: 2}, 2), "单档沿用原路径")
	require.Nil(t, imageBillingTierCounts(map[string]int{ImageBillingSize1K: 1, ImageBillingSize4K: 1}, 3), "张数不符回退")
	require.Equal(t,
		map[string]int{ImageBillingSize1K: 1, ImageBillingSize4K: 2},
		imageBillingTierCounts(map[string]int{ImageBillingSize1K: 1, ImageBillingSize4K: 2}, 3),
	)
}
