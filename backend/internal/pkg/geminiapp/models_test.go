//go:build unit

package geminiapp

import (
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsNativeModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{ModelArgon, true},
		{ModelHydrogen, true},
		{ModelFlashLite, true},
		{"gemini-pro", false},
		{"models/gemini-argon", false},
		{"", false},
		{"GEMINI-ARGON", false},
		{"gemini-argon ", false},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			require.Equal(t, test.want, IsNativeModel(test.model))
		})
	}
}

func TestNativeModelsOrdering(t *testing.T) {
	require.Equal(t, []string{ModelArgon, ModelHydrogen, ModelFlashLite}, NativeModels)
	require.Len(t, NativeModels, len(ModelModes))
	seen := map[string]struct{}{}
	for _, model := range NativeModels {
		require.True(t, IsNativeModel(model), "listed model %q is not native", model)
		_, duplicate := seen[model]
		require.False(t, duplicate, "duplicate model %q", model)
		seen[model] = struct{}{}
	}
	require.Equal(t, len(NativeModels), len(seen))
}

func TestModelModesTable(t *testing.T) {
	require.Equal(t, map[string]int{
		ModelArgon:     ModeUltra,
		ModelHydrogen:  ModeFast,
		ModelFlashLite: ModeFlashLite,
	}, ModelModes)
	require.Equal(t, 8, ModeUltra)
	require.Equal(t, 1, ModeFast)
	require.Equal(t, 6, ModeFlashLite)
}

func TestReasoningEffortLevelTable(t *testing.T) {
	expected := map[string]int{
		"none":    ThinkingNone,
		"minimal": ThinkingNone,
		"low":     ThinkingLow,
		"medium":  ThinkingExtended,
		"high":    ThinkingExtended,
		"xhigh":   ThinkingExtended,
	}
	require.Equal(t, expected, ReasoningEffortLevel)
	for effort, level := range ReasoningEffortLevel {
		require.NotEqual(t, ThinkingDeep, level, "effort %q must not map to async deep think", effort)
	}
	_, ok := ReasoningEffortLevel["ultra"]
	require.False(t, ok)
}

func TestThinkingLevelConstants(t *testing.T) {
	require.Equal(t, 0, ThinkingNone)
	require.Equal(t, 1, ThinkingLow)
	require.Equal(t, 2, ThinkingExtended)
	require.Equal(t, 3, ThinkingDeep)
	// 4 是最高同步档位；更高级别在协议里不存在，解析层必须拒绝 > 4。
	require.Equal(t, 4, ThinkingMax)
	require.Greater(t, ThinkingMax, ThinkingDeep)
}

func TestNewUUIDFormatAndUniqueness(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		uuid, err := NewUUID()
		require.NoError(t, err)
		require.Regexp(t, pattern, uuid)
		require.Len(t, uuid, 36)
		_, duplicate := seen[uuid]
		require.False(t, duplicate, "duplicate UUID %q", uuid)
		seen[uuid] = struct{}{}
	}
}

func TestNewHTTPClientDefaults(t *testing.T) {
	client, err := NewHTTPClient("")
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Zero(t, client.Timeout)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, transport.ForceAttemptHTTP2)
	require.Nil(t, transport.Proxy)
	require.Equal(t, 16, transport.MaxIdleConns)
}

func TestNewHTTPClientWithProxy(t *testing.T) {
	client, err := NewHTTPClient("http://127.0.0.1:3128")
	require.NoError(t, err)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy)

	proxyURL, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:3128", proxyURL.Host)
	require.Equal(t, "http", proxyURL.Scheme)
}

func TestNewHTTPClientRejectsInvalidProxy(t *testing.T) {
	for _, proxy := range []string{"not-a-url", "http://", "://missing-scheme", "relative/path"} {
		t.Run(proxy, func(t *testing.T) {
			client, err := NewHTTPClient(proxy)
			require.Nil(t, client)
			requireProtocolError(t, err, "INVALID_PROXY_URL", 400)
		})
	}
}
