package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// writeGeminiAppOAuthError 把 geminiapp.Error 携带的上游状态码映射到响应；
// response.ErrorFrom 只识别 ApplicationError，否则 409/400 会被压成 500。
func writeGeminiAppOAuthError(c *gin.Context, err error) {
	if appErr, ok := geminiapp.AsError(err); ok {
		status := appErr.Status
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadRequest
		}
		response.ErrorWithDetails(c, status, appErr.Error(), appErr.Code, nil)
		return
	}
	response.ErrorFrom(c, err)
}

// GeminiAppOAuthHandler 提供 Gemini App（Google OAuth）账号的授权与 token 接口。
type GeminiAppOAuthHandler struct {
	geminiAppOAuthService *service.GeminiAppOAuthService
}

func NewGeminiAppOAuthHandler(geminiAppOAuthService *service.GeminiAppOAuthService) *GeminiAppOAuthHandler {
	return &GeminiAppOAuthHandler{geminiAppOAuthService: geminiAppOAuthService}
}

// GetCapabilities 返回当前部署是否配置了 OAuth client profile。
// GET /api/v1/admin/gemini-app/oauth/capabilities
func (h *GeminiAppOAuthHandler) GetCapabilities(c *gin.Context) {
	response.Success(c, h.geminiAppOAuthService.Capabilities())
}

type GeminiAppGenerateAuthURLRequest struct {
	ProxyID *int64 `json:"proxy_id"`
}

// GenerateAuthURL 生成 Google OAuth 授权链接。
// POST /api/v1/admin/gemini-app/oauth/auth-url
func (h *GeminiAppOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req GeminiAppGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.geminiAppOAuthService.GenerateAuthURL(c.Request.Context(), req.ProxyID)
	if err != nil {
		writeGeminiAppOAuthError(c, err)
		return
	}

	response.Success(c, result)
}

type GeminiAppExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	State     string `json:"state" binding:"required"`
	Code      string `json:"code" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// ExchangeCode 用 authorization code 交换 token。
// POST /api/v1/admin/gemini-app/oauth/exchange-code
func (h *GeminiAppOAuthHandler) ExchangeCode(c *gin.Context) {
	var req GeminiAppExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.geminiAppOAuthService.ExchangeCode(c.Request.Context(), &service.GeminiAppExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		writeGeminiAppOAuthError(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

type GeminiAppRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
	ProxyID      *int64 `json:"proxy_id"`
}

// RefreshToken 验证 refresh_token 并返回新的 token 信息。
// POST /api/v1/admin/gemini-app/oauth/refresh-token
func (h *GeminiAppOAuthHandler) RefreshToken(c *gin.Context) {
	var req GeminiAppRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	proxyURL := ""
	if req.ProxyID != nil {
		proxyURL = h.geminiAppOAuthService.ProxyURLForID(c.Request.Context(), *req.ProxyID)
	}
	tokenInfo, err := h.geminiAppOAuthService.RefreshToken(c.Request.Context(), req.RefreshToken, proxyURL)
	if err != nil {
		writeGeminiAppOAuthError(c, err)
		return
	}

	response.Success(c, tokenInfo)
}
