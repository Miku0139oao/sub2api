//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 混合 chat/messages 请求即使带回内嵌图片，也不属于专用图片端点：
// 分组 image_count_billing 不能把整条请求按张结算。
func TestGeminiRequestIsImageEndpointRequiresExplicitIntent(t *testing.T) {
	textBody := []byte(`{
		"contents": [{"role":"user","parts":[{"text":"describe this"}]}],
		"generationConfig": {"responseModalities": ["TEXT"]}
	}`)
	imageBody := []byte(`{
		"contents": [{"role":"user","parts":[{"text":"draw a cat"}]}],
		"generationConfig": {"responseModalities": ["TEXT","IMAGE"]}
	}`)
	snakeCaseImageBody := []byte(`{
		"contents": [{"role":"user","parts":[{"text":"draw a cat"}]}],
		"generation_config": {"response_modalities": ["IMAGE"]}
	}`)

	require.False(t, geminiRequestIsImageEndpoint(textBody, "gemini-3-pro"),
		"普通文本请求不是图片端点")
	require.True(t, geminiRequestIsImageEndpoint(imageBody, "gemini-3-pro"),
		"responseModalities 含 IMAGE 是图片端点")
	require.True(t, geminiRequestIsImageEndpoint(snakeCaseImageBody, "gemini-3-pro"),
		"snake_case 写法也要识别")
	require.True(t, geminiRequestIsImageEndpoint(textBody, "gemini-3-pro-image"),
		"图片模型即使没写 modalities 也是图片端点")
	require.True(t, geminiRequestIsImageEndpoint(textBody, "alias-name", "gemini-2.5-flash-image"),
		"映射后的上游模型名同样生效")
	require.False(t, geminiRequestIsImageEndpoint(nil, "gemini-3-pro"))
}

// 内嵌图片计数不能反过来决定端点类型：count>0 但无显式意图时仍是混合请求。
func TestGeminiImageCountDoesNotImplyImageEndpoint(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"summarize"}]}]}`)
	endpoint := geminiRequestIsImageEndpoint(body, "gemini-3-pro")
	require.False(t, endpoint)
}
