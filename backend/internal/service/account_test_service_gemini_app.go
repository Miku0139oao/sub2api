package service

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/gin-gonic/gin"
)

func (s *AccountTestService) testGeminiAppAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	if s.geminiAppGatewayService == nil {
		return s.sendErrorAndEnd(c, "Gemini App service is not configured")
	}
	if account == nil || account.Platform != PlatformGeminiApp || account.Type != AccountTypeOAuth {
		return s.sendErrorAndEnd(c, "Gemini App accounts require OAuth credentials")
	}
	model := strings.TrimSpace(modelID)
	if model == "" {
		model = geminiapp.ModelArgon
	}
	if !geminiapp.IsNativeModel(model) {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported model: %s", model))
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})

	content, err := s.geminiAppGatewayService.RunTestTurn(c.Request.Context(), account, model, prompt)
	if err != nil {
		return s.sendErrorAndEnd(c, sanitizeUpstreamErrorMessage(err.Error()))
	}
	if strings.TrimSpace(content) == "" {
		content = "Gemini App account is healthy."
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: content})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
