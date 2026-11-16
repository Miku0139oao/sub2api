//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Gemini App 的原生模型别名（gemini-argon / gemini-hydrogen / gemini-flash-lite）
// 不是 Google 官方模型 ID，历史表格里也没有对应条目。缺了别名映射时计费服务
// 会拿不到价格，token 请求会被记成 0 成本（fail-open），必须钉死映射关系。
func TestGetFallbackPricing_GeminiAppNativeAliases(t *testing.T) {
	svc := newTestBillingService()

	tests := []struct {
		model string
		card  string
	}{
		{model: "gemini-argon", card: "gemini-4-argon"},
		{model: "gemini-hydrogen", card: "gemini-3.8-flash"},
		{model: "gemini-flash-lite", card: "gemini-flash-lite"},
	}

	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			want := svc.fallbackPrices[test.card]
			require.NotNil(t, want, "fallback price card %q must exist", test.card)

			got := svc.getFallbackPricing(test.model)
			require.NotNil(t, got, "%s must resolve to a fallback price card", test.model)
			require.Same(t, want, got, "%s must reuse the %q card", test.model, test.card)
			require.Positive(t, got.InputPricePerToken)
			require.Positive(t, got.OutputPricePerToken)
		})
	}

	// 官方价卡必须保持字面值，避免将来重构把促销价误改。
	argon := svc.getFallbackPricing("gemini-argon")
	require.InDelta(t, 2e-6, argon.InputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, argon.OutputPricePerToken, 1e-12)
	require.InDelta(t, 0.1e-6, argon.CacheReadPricePerToken, 1e-12)

	hydrogen := svc.getFallbackPricing("gemini-hydrogen")
	require.InDelta(t, 0.75e-6, hydrogen.InputPricePerToken, 1e-12)
	require.InDelta(t, 3.75e-6, hydrogen.OutputPricePerToken, 1e-12)

	flashLite := svc.getFallbackPricing("gemini-flash-lite")
	require.InDelta(t, 0.1e-6, flashLite.InputPricePerToken, 1e-12)
	require.InDelta(t, 0.4e-6, flashLite.OutputPricePerToken, 1e-12)
	require.InDelta(t, 0.01e-6, flashLite.CacheReadPricePerToken, 1e-12)
}

// 别名一旦解析到价卡，CalculateCost 就必须产出非零成本；否则 usage log 的
// pricing_missing_record_zero_cost 兜底会掩盖 Gemini App 的漏计费。
func TestCalculateCost_GeminiAppNativeAliasesAreNotFree(t *testing.T) {
	svc := newTestBillingService()

	for _, model := range []string{"gemini-argon", "gemini-hydrogen", "gemini-flash-lite"} {
		t.Run(model, func(t *testing.T) {
			cost, err := svc.CalculateCost(model, UsageTokens{InputTokens: 1000, OutputTokens: 500}, 1.0)
			require.NoError(t, err)
			require.Positive(t, cost.InputCost, "%s input must be billed", model)
			require.Positive(t, cost.OutputCost, "%s output must be billed", model)
			require.InDelta(t, cost.InputCost+cost.OutputCost, cost.TotalCost, 1e-12)
		})
	}
}
