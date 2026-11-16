//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Gemini App 账号只有 OAuth 一种类型，分组因此可以开启 OAuth-only 过滤。
// 漏掉这个平台会让分组误放行非 OAuth 账号，调度时才发现凭证不匹配。
func TestGroupSupportsOAuthOnlyFilter_GeminiApp(t *testing.T) {
	require.True(t, groupSupportsOAuthOnlyFilter(PlatformGeminiApp))

	for _, platform := range []string{PlatformOpenAI, PlatformAntigravity, PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformComposite} {
		require.True(t, groupSupportsOAuthOnlyFilter(platform), platform)
	}
	require.False(t, groupSupportsOAuthOnlyFilter(PlatformTypeSafe))
}
