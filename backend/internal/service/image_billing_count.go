package service

// maxBillableImageCountPerRequest 上限拦的是「解析不到实际张数、只能信任客户端 n」
// 的兜底路径：官方单次请求最多 10 张，异常的超大 n 不应该变成账单数字。
// 响应里真实返回的张数（reported）不受此限制。
const maxBillableImageCountPerRequest = 10

// resolveBillableImageCount 决定一次图片请求按几张计费。
//
// 官方多图响应没有 n 字段，只以响应体 data/output 中实际返回的图片为准。
// 解析不到张数时：
//   - 分组开启按张计费：按 1 张兜底，避免已成功出图的请求完全漏计；
//   - 未开启：流式按 0 张（没观测到图片就不扣），非流式回退请求 n。
//
// 回退到请求 n 时按 maxBillableImageCountPerRequest 收敛，避免客户端传超大 n
// 在解析失败时被当成实际出图数多扣。reported 是上游真实张数，保持原样。
func resolveBillableImageCount(reported, requested int, fallbackToOne bool) int {
	if reported > 0 {
		return reported
	}
	if fallbackToOne {
		return 1
	}
	if requested > 0 {
		if requested > maxBillableImageCountPerRequest {
			return maxBillableImageCountPerRequest
		}
		return requested
	}
	return 0
}
