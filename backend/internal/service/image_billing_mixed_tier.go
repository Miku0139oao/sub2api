package service

import "strings"

// imageCostUsesTokenBilling 判断本次图片请求最终是否按 token 结算。
//
// 只有「渠道定价为 token 模式、且分组未强制按张」时才会走 token 路径；其余情况
// （分组按次/图片定价、分组图片价、渠道按次/图片定价、无定价回退默认图片价）
// 都是按张结算，可以安全地逐尺寸档拆分。
func imageCostUsesTokenBilling(resolved *ResolvedPricing, forcePerImage bool) bool {
	return resolved != nil &&
		resolved.Source == PricingSourceChannel &&
		resolved.Mode == BillingModeToken &&
		!forcePerImage
}

// imageBillingTierCounts 返回按尺寸档拆分的图片张数。
//
// 响应里只有一种尺寸档时返回 nil：调用方沿用原来的单档路径，保留渠道/分组按次
// 定价、分组图片价和默认图片价各自的查找顺序。只有出现两种及以上尺寸档时才返回
// 拆分结果，让调用方逐档计价——否则 1 张 1K + 1 张 4K 会整体按 4K 单价结算。
//
// 返回的 map 只包含可识别的档位（1K/2K/4K），张数为正；合计张数必须等于
// imageCount，否则视为数据不完整并返回 nil，回退到原有的单档行为。
func imageBillingTierCounts(breakdown map[string]int, imageCount int) map[string]int {
	if len(breakdown) < 2 || imageCount <= 0 {
		return nil
	}
	counts := make(map[string]int, len(breakdown))
	total := 0
	for tier, count := range breakdown {
		normalized := strings.ToUpper(strings.TrimSpace(tier))
		if imageTierRank(normalized) == 0 || count <= 0 {
			continue
		}
		counts[normalized] += count
		total += count
	}
	if len(counts) < 2 || total != imageCount {
		return nil
	}
	return counts
}

// forEachImageBillingTier 按档位顺序执行计价闭包并汇总。
// cost(tier, count) 返回该档的费用；任一档出错则整体返回错误，由调用方回退。
func forEachImageBillingTier(
	counts map[string]int,
	cost func(tier string, count int) (*CostBreakdown, error),
) (*CostBreakdown, error) {
	merged := &CostBreakdown{}
	for _, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
		count := counts[tier]
		if count <= 0 {
			continue
		}
		part, err := cost(tier, count)
		if err != nil {
			return nil, err
		}
		if part == nil {
			continue
		}
		merged.InputCost += part.InputCost
		merged.ImageInputCost += part.ImageInputCost
		merged.OutputCost += part.OutputCost
		merged.ImageOutputCost += part.ImageOutputCost
		merged.CacheCreationCost += part.CacheCreationCost
		merged.CacheReadCost += part.CacheReadCost
		merged.TotalCost += part.TotalCost
		merged.ActualCost += part.ActualCost
		if part.BillingMode != "" {
			merged.BillingMode = part.BillingMode
		}
		merged.LongContextBillingApplied = merged.LongContextBillingApplied || part.LongContextBillingApplied
	}
	return merged, nil
}
