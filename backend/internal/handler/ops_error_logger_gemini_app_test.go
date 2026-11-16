//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGuessPlatformFromPath_GeminiApp(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/gemini-app/v1/chat/completions", want: service.PlatformGeminiApp},
		{path: "/gemini-app/v1/responses", want: service.PlatformGeminiApp},
		{path: "/GEMINI-APP/v1/models", want: service.PlatformGeminiApp},
		{path: "/v1/chat/completions", want: ""},
		{path: "/antigravity/v1/messages", want: service.PlatformAntigravity},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			require.Equal(t, test.want, guessPlatformFromPath(test.path))
		})
	}

	// gemini-app 前缀不得被更宽泛的 gemini 判定吞掉。
	require.NotEqual(t, service.PlatformGemini, guessPlatformFromPath("/gemini-app/v1/responses"))
}
