//go:build unit

package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 余额 hold/capture 与其它计费金额一样必须先量化到 NUMERIC(20,8)，
// 否则 hold 与 capture 在存储层独立舍入，冻结余额会留下 1e-8 残差。
func TestBatchImageBalanceHoldCommandNormalizeQuantizesAmounts(t *testing.T) {
	cmd := &BatchImageBalanceHoldCommand{
		RequestID:    "hold-1",
		APIKeyID:     1,
		UserID:       2,
		BatchID:      "batch-1",
		HoldAmount:   0.000078125,
		ActualAmount: 0.000078125,
	}
	cmd.Normalize()

	require.Equal(t, 0.00007813, cmd.HoldAmount)
	require.Equal(t, 0.00007813, cmd.ActualAmount)
	require.Equal(t, QuantizeUsageBillingAmount(0.000078125), cmd.HoldAmount)
}

// 指纹仍由金额派生，量化后重新计算的结果必须稳定（幂等重试不能变指纹）。
func TestBatchImageBalanceHoldCommandNormalizeIsIdempotent(t *testing.T) {
	cmd := &BatchImageBalanceHoldCommand{
		RequestID:  "hold-2",
		APIKeyID:   1,
		UserID:     2,
		BatchID:    "batch-2",
		HoldAmount: 1.2345678901,
	}
	cmd.Normalize()
	firstFingerprint := cmd.RequestFingerprint
	firstHold := cmd.HoldAmount

	cmd.Normalize()
	require.Equal(t, firstHold, cmd.HoldAmount)
	require.Equal(t, firstFingerprint, cmd.RequestFingerprint)
}

// 计费后处理必须有并发上限；超限时返回 false 让调用方同步兜底，不静默丢弃。
func TestTryRunPostBillingRespectsConcurrencyLimit(t *testing.T) {
	var running atomic.Int64
	var wg sync.WaitGroup
	block := make(chan struct{})
	started := make(chan struct{}, postBillingConcurrencyLimit+8)

	accepted := 0
	for i := 0; i < postBillingConcurrencyLimit+8; i++ {
		wg.Add(1)
		if tryRunPostBilling(func() {
			defer wg.Done()
			running.Add(1)
			started <- struct{}{}
			<-block
		}) {
			accepted++
		} else {
			wg.Done()
		}
	}

	require.Equal(t, postBillingConcurrencyLimit, accepted, "超出的调用必须同步执行")
	// 等所有已启动任务真正进入函数体，确保 running 达到上限。
	for i := 0; i < accepted; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("post-billing task did not start")
		}
	}
	require.Equal(t, int64(postBillingConcurrencyLimit), running.Load())

	close(block)
	wg.Wait()
	require.Eventually(t, func() bool { return postBillingRunning.Load() == 0 }, 2*time.Second, 10*time.Millisecond)
}
