//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 客户端可以任意填写 n；解析不到上游实际张数时会回退用 n 计费，
// 因此解析阶段就收敛到官方上限，避免异常大的 n 进入账单。
func TestApplyOpenAIImagesDefaultsClampsN(t *testing.T) {
	req := &OpenAIImagesRequest{N: 500}
	applyOpenAIImagesDefaults(req)
	require.Equal(t, maxBillableImageCountPerRequest, req.N)

	defaulted := &OpenAIImagesRequest{}
	applyOpenAIImagesDefaults(defaulted)
	require.Equal(t, 1, defaulted.N)

	normal := &OpenAIImagesRequest{N: 4}
	applyOpenAIImagesDefaults(normal)
	require.Equal(t, 4, normal.N)
}

func TestParseOpenAIImagesJSONRequestKeepsRequestedNAndClampsLater(t *testing.T) {
	req := &OpenAIImagesRequest{}
	require.NoError(t, parseOpenAIImagesJSONRequest([]byte(`{"prompt":"x","n":500}`), req))
	require.Equal(t, 500, req.N, "解析阶段保留原值，由 defaults 收敛")

	applyOpenAIImagesDefaults(req)
	require.Equal(t, maxBillableImageCountPerRequest, req.N)
}
