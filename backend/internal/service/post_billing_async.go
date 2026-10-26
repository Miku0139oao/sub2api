package service

import (
	"runtime"
	"sync/atomic"
)

// postBillingConcurrencyLimit 限制计费后处理的并发度（平台配额落库 + 通知检查）。
//
// 旧实现每笔已计费请求直接 `go func()`：一次流量尖峰会在极短时间内创建
// 2~3 个 goroutine/请求去做 DB 与邮件工作，既没有上限也没有排队，和本仓库
// 其它异步路径（UsageRecordWorkerPool）的口径不一致。
const postBillingConcurrencyLimit = 64

var postBillingRunning atomic.Int64
var postBillingDropped atomic.Int64

// tryRunPostBilling 在并发未达上限时异步执行 fn，否则返回 false 由调用方决定
// 同步兜底还是跳过。fn 内部必须自行 recover。
func tryRunPostBilling(fn func()) bool {
	limit := int64(postBillingConcurrencyLimit)
	if limit <= 0 {
		limit = int64(runtime.GOMAXPROCS(0) * 4)
	}
	if postBillingRunning.Add(1) > limit {
		postBillingRunning.Add(-1)
		postBillingDropped.Add(1)
		return false
	}
	go func() {
		defer postBillingRunning.Add(-1)
		fn()
	}()
	return true
}

// PostBillingAsyncStats 暴露给运维面板，用于判断是否长期处于限流状态。
func PostBillingAsyncStats() (running, dropped int64) {
	return postBillingRunning.Load(), postBillingDropped.Load()
}
