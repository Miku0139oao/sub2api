package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

const (
	GeminiAppUpstreamEndpoint = "/gemini-app/ProcessQuery"
	geminiAppDefaultTurnLimit = 180 * time.Second
	geminiAppMaxToolCalls     = 64
)

// GeminiAppCredentialRejectedReason marks a Gemini App OAuth credential that the
// upstream refused. Keeping it separate from Antigravity's reason keeps ops
// dashboards readable and lets the scheduler treat the account correctly.
const GeminiAppCredentialRejectedReason GatewayFailureReason = "gemini_app_oauth_credential_rejected"

// GeminiAppCredentialRejectedClientMessage is the client-facing message for the
// reason above; handlers must not fall back to another platform's wording.
const GeminiAppCredentialRejectedClientMessage = "Gemini App credential rejected by upstream"

// GeminiAppGatewayService 通过 Gemini App 原生 ProcessQuery 协议转发
// OpenAI Chat Completions / Responses 请求。
//
// 上游只提供文本生成，没有原生函数调用：工具通过提示词协议（```tool_call 围栏）
// 表达，再由本服务切分并还原为 OpenAI 的 tool_calls。
type GeminiAppGatewayService struct {
	tokenProvider *GeminiAppTokenProvider
	proxyRepo     ProxyRepository
	clients       sync.Map // proxyURL -> *http.Client
}

// NewGeminiAppGatewayService 创建 gateway 服务。
func NewGeminiAppGatewayService(tokenProvider *GeminiAppTokenProvider, proxyRepo ProxyRepository) *GeminiAppGatewayService {
	return &GeminiAppGatewayService{tokenProvider: tokenProvider, proxyRepo: proxyRepo}
}

// ForwardAsChatCompletions 以 Chat Completions 形态转发请求。
func (s *GeminiAppGatewayService) ForwardAsChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*ForwardResult, error) {
	return s.forward(ctx, c, account, body, geminiAppProtocolChatCompletions)
}

// ForwardAsResponses 以 Responses 形态转发请求。
func (s *GeminiAppGatewayService) ForwardAsResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*ForwardResult, error) {
	return s.forward(ctx, c, account, body, geminiAppProtocolResponses)
}

// TestConnectionWithPrompt 用固定提示词验证账号可用性（账号测试用）。
func (s *GeminiAppGatewayService) TestConnectionWithPrompt(ctx context.Context, account *Account, prompt string) error {
	if s == nil || s.tokenProvider == nil {
		return errors.New("gemini app token provider not configured")
	}
	if account == nil || account.Platform != PlatformGeminiApp {
		return errors.New("not a gemini app account")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return err
	}
	client, err := s.httpClient(geminiAppProxyURL(ctx, account, s.proxyRepo))
	if err != nil {
		return err
	}
	canonical := &geminiapp.CanonicalRequest{
		Model:         geminiapp.ModelArgon,
		ThinkingLevel: geminiapp.ThinkingNone,
		Messages:      []geminiapp.ChatMessage{{Role: "user", Content: prompt}},
	}
	built, err := geminiapp.BuildCanonicalPrompt(canonical)
	if err != nil {
		return err
	}
	turn := newGeminiAppTurn(geminiapp.ModelArgon, geminiapp.ModeUltra, geminiapp.ThinkingNone, nil, nil)
	return turn.run(ctx, nativeTurnInput{
		accessToken: accessToken,
		prompt:      built.Text,
		model:       geminiapp.ModelArgon,
		client:      client,
	})
}

func (s *GeminiAppGatewayService) forward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	protocol geminiAppProtocol,
) (*ForwardResult, error) {
	startTime := time.Now()
	if account == nil {
		return nil, writeGeminiAppError(c, protocol, http.StatusBadRequest, "invalid_request_error", "account is required")
	}
	if account.Platform != PlatformGeminiApp {
		return nil, writeGeminiAppError(c, protocol, http.StatusBadRequest, "invalid_request_error", "account is not a gemini app account")
	}
	if account.Type != AccountTypeOAuth {
		return nil, writeGeminiAppError(c, protocol, http.StatusBadRequest, "invalid_request_error", "gemini app accounts require OAuth credentials")
	}
	if s == nil || s.tokenProvider == nil {
		return nil, writeGeminiAppError(c, protocol, http.StatusBadGateway, "upstream_error", "Gemini App is not configured")
	}

	parsed, err := geminiapp.ParseBoundedJSON(string(body), http.StatusBadRequest)
	if err != nil {
		return nil, writeGeminiAppMappedError(c, protocol, err)
	}
	payload, ok := parsed.(map[string]any)
	if !ok {
		return nil, writeGeminiAppError(c, protocol, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object")
	}

	var canonical *geminiapp.CanonicalRequest
	switch protocol {
	case geminiAppProtocolResponses:
		canonical, err = geminiapp.NormalizeResponsesRequest(payload)
	default:
		canonical, err = geminiapp.NormalizeChatRequest(payload)
	}
	if err != nil {
		return nil, writeGeminiAppMappedError(c, protocol, err)
	}
	built, err := geminiapp.BuildCanonicalPrompt(canonical)
	if err != nil {
		return nil, writeGeminiAppMappedError(c, protocol, err)
	}

	stream := canonical.Stream
	responseID := geminiAppResponseID(protocol)
	c.Header("X-Upstream-Endpoint", GeminiAppUpstreamEndpoint)

	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		// token 刷新失败属于账号级凭证问题，交给 failover 切换账号。
		return nil, &UpstreamFailoverError{
			StatusCode:        http.StatusBadGateway,
			Stage:             GatewayFailureStageAccountAuth,
			Scope:             GatewayFailureScopeAccount,
			Reason:            GeminiAppCredentialRejectedReason,
			NextAccountAction: NextAccountRetry,
			ClientStatusCode:  http.StatusBadGateway,
			ClientMessage:     GeminiAppCredentialRejectedClientMessage,
		}
	}

	client, err := s.httpClient(geminiAppProxyURL(ctx, account, s.proxyRepo))
	if err != nil {
		return nil, writeGeminiAppMappedError(c, protocol, err)
	}

	renderer := newGeminiAppRenderer(protocol, c, canonical, startTime, responseID)
	renderer.stream = stream
	renderer.includeUsage = canonical.IncludeUsage
	if stream {
		if err := renderer.beginStream(); err != nil {
			return nil, err
		}
	}

	parallelDisabled := canonical.ParallelToolCalls != nil && !*canonical.ParallelToolCalls
	turn := newGeminiAppTurn(canonical.Model, geminiapp.ModelModes[canonical.Model], canonical.ThinkingLevel, canonical.Policy, renderer.emit)
	turn.parallelDisabled = parallelDisabled
	renderer.turn = turn
	turnResult, turnErr := turn.runWithResult(ctx, nativeTurnInput{
		accessToken: accessToken,
		prompt:      built.Text,
		model:       canonical.Model,
		client:      client,
	})
	if turnErr != nil {
		return nil, renderer.fail(turnErr)
	}

	forwardResult, err := renderer.finish(turnResult, built.Text)
	if err != nil {
		return nil, err
	}
	forwardResult.Duration = time.Since(startTime)
	forwardResult.FirstTokenMs = turn.firstTokenMs(startTime)
	forwardResult.Stream = stream
	forwardResult.ClientDisconnect = renderer.disconnected()
	forwardResult.Model = canonical.Model
	forwardResult.RequestID = responseID
	forwardResult.ReasoningEffort = geminiAppReasoningEffort(canonical)
	return forwardResult, nil
}

func (s *GeminiAppGatewayService) httpClient(proxyURL string) (*http.Client, error) {
	if cached, ok := s.clients.Load(proxyURL); ok {
		if client, ok := cached.(*http.Client); ok {
			return client, nil
		}
	}
	client, err := geminiapp.NewHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	actual, _ := s.clients.LoadOrStore(proxyURL, client)
	if cached, ok := actual.(*http.Client); ok {
		return cached, nil
	}
	return client, nil
}

func geminiAppReasoningEffort(canonical *geminiapp.CanonicalRequest) *string {
	if canonical == nil || strings.TrimSpace(canonical.ReasoningEffort) == "" {
		return nil
	}
	effort := canonical.ReasoningEffort
	return &effort
}

func geminiAppResponseID(protocol geminiAppProtocol) string {
	uuid, err := geminiapp.NewUUID()
	if err != nil {
		uuid = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if protocol == geminiAppProtocolResponses {
		return "resp_" + strings.ReplaceAll(uuid, "-", "")
	}
	return "chatcmpl-" + strings.ReplaceAll(uuid, "-", "")
}

type geminiAppProtocol int

const (
	geminiAppProtocolChatCompletions geminiAppProtocol = iota
	geminiAppProtocolResponses
)

// ---------------------------------------------------------------------------
// Native turn engine
// ---------------------------------------------------------------------------

type nativeTurnInput struct {
	accessToken string
	prompt      string
	model       string
	client      *http.Client
}

type geminiAppTurnResult struct {
	Content  string
	Calls    []geminiapp.ToolCall
	Frames   int
	Response string
	Thinking *uint64
	Mode     *uint64
}

// geminiAppTurn 累积上游文本快照，把新增文本喂给工具围栏切分器，并按事件发布。
type geminiAppTurn struct {
	model            string
	mode             int
	thinking         int
	policy           *geminiapp.ToolPolicy
	splitter         *geminiapp.ToolCallSplitter
	emit             func(geminiapp.ToolEvent) error
	responseID       string
	parallelDisabled bool

	rawText         string
	publishedText   string
	synced          int
	completed       bool
	observedMode    *uint64
	thinkingLevel   *uint64
	fallback        bool
	downgradeReason *uint64
	frames          int
	publishedBytes  int
	publishedCalls  int
	content         strings.Builder
	calls           []geminiapp.ToolCall
	firstContentAt  time.Time
}

func newGeminiAppTurn(
	model string,
	mode int,
	thinking int,
	policy *geminiapp.ToolPolicy,
	emit func(geminiapp.ToolEvent) error,
) *geminiAppTurn {
	return &geminiAppTurn{
		model:    model,
		mode:     mode,
		thinking: thinking,
		policy:   policy,
		splitter: geminiapp.NewToolCallSplitter(policy),
		emit:     emit,
	}
}

func (t *geminiAppTurn) run(ctx context.Context, input nativeTurnInput) error {
	_, err := t.runWithResult(ctx, input)
	return err
}

func (t *geminiAppTurn) runWithResult(ctx context.Context, input nativeTurnInput) (*geminiAppTurnResult, error) {
	clientInstanceID, err := geminiapp.NewUUID()
	if err != nil {
		return nil, geminiapp.NewError("NATIVE_CLIENT_ID_FAILED", 502)
	}
	_, turnErr := geminiapp.ProcessQuery(ctx, geminiapp.TurnOptions{
		AccessToken:      input.accessToken,
		Prompt:           input.prompt,
		ModeCategory:     t.mode,
		ThinkingLevel:    t.thinking,
		TemporaryChat:    true,
		ClientInstanceID: clientInstanceID,
		Timeout:          geminiAppDefaultTurnLimit,
		HTTPClient:       input.client,
	}, func(frame []byte) error {
		info, err := geminiapp.DecodeFrame(frame)
		if err != nil {
			return err
		}
		return t.ingest(info)
	})
	if turnErr != nil {
		return nil, turnErr
	}
	return t.finalize()
}

// ingest 消费一个解码后的原生响应帧。
func (t *geminiAppTurn) ingest(info *geminiapp.GenerateInfo) error {
	if info == nil {
		return nil
	}
	t.frames++
	if info.HasToolOperations {
		// 产品路径使用提示词工具协议；原生函数调用帧不应出现。
		return geminiapp.NewError("NATIVE_FUNCTION_CALL_UNSUPPORTED", 502)
	}
	if info.Fallback != nil && *info.Fallback != 0 {
		t.fallback = true
	}
	if info.DowngradeReason != nil && *info.DowngradeReason != 0 {
		t.downgradeReason = info.DowngradeReason
	}
	if info.ThinkingLevel != nil {
		if *info.ThinkingLevel > 4 {
			return geminiapp.NewError("INVALID_NATIVE_THINKING_LEVEL", 502)
		}
		t.thinkingLevel = info.ThinkingLevel
	}
	if info.HasText {
		if err := t.applySnapshot(info); err != nil {
			return err
		}
	}
	if info.Completed {
		t.completed = true
	}
	if info.ResolvedModeCategory != nil {
		hasContent := info.HasText && len(info.Text) > 0
		if *info.ResolvedModeCategory != 0 || hasContent || t.observedMode == nil {
			t.observedMode = info.ResolvedModeCategory
		}
	}
	if t.downgraded() {
		return t.downgradeError()
	}
	if !t.modeVerified() {
		// 未确认模型前不发布任何内容，避免先输出再报错。
		return nil
	}
	return t.publish()
}

func (t *geminiAppTurn) applySnapshot(info *geminiapp.GenerateInfo) error {
	if t.responseID != "" && info.ResponseID != "" && info.ResponseID != t.responseID {
		return geminiapp.NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	extends := strings.HasPrefix(info.Text, t.rawText)
	if !extends {
		if t.publishedBytes > 0 || t.publishedCalls > 0 {
			return geminiapp.NewError("NON_MONOTONIC_NATIVE_RESPONSE", 502)
		}
		if info.ResponseID == "" || (t.responseID != "" && info.ResponseID != t.responseID) {
			return geminiapp.NewError("NON_MONOTONIC_NATIVE_RESPONSE", 502)
		}
		// 尚未对外发布任何内容：允许整体替换快照并重建切分状态。
		t.splitter = geminiapp.NewToolCallSplitter(t.policy)
		t.synced = 0
		t.content.Reset()
		t.calls = nil
	}
	if info.ResponseID != "" {
		t.responseID = info.ResponseID
	}
	t.rawText = info.Text
	return nil
}

func (t *geminiAppTurn) publish() error {
	if t.synced >= len(t.rawText) {
		return nil
	}
	delta := t.rawText[t.synced:]
	t.synced = len(t.rawText)
	events, err := t.splitter.Push(delta)
	if err != nil {
		return err
	}
	return t.handleEvents(events)
}

func (t *geminiAppTurn) handleEvents(events []geminiapp.ToolEvent) error {
	for _, event := range events {
		switch event.Type {
		case "content":
			if event.Text == "" {
				continue
			}
			t.content.WriteString(event.Text)
		case "call":
			if event.Call == nil {
				continue
			}
			t.calls = append(t.calls, *event.Call)
			t.publishedCalls++
			if t.emit != nil {
				if err := t.emit(event); err != nil {
					return err
				}
			}
		}
	}
	return t.publishPreview()
}

// publishPreview 把切分器确认安全的前瞻文本推给客户端。
// 与上游一致：可见文本 = 已确认内容 + preview；只发布增量。
func (t *geminiAppTurn) publishPreview() error {
	visible := t.content.String() + t.splitter.Preview()
	if len(visible) <= len(t.publishedText) {
		return nil
	}
	if !strings.HasPrefix(visible, t.publishedText) {
		return geminiapp.NewError("NON_MONOTONIC_NATIVE_RESPONSE", 502)
	}
	delta := visible[len(t.publishedText):]
	t.publishedText = visible
	t.publishedBytes += len(delta)
	if t.firstContentAt.IsZero() {
		t.firstContentAt = time.Now()
	}
	if t.emit != nil {
		return t.emit(geminiapp.ToolEvent{Type: "content", Text: delta})
	}
	return nil
}

func (t *geminiAppTurn) finalize() (*geminiAppTurnResult, error) {
	events, err := t.splitter.Flush()
	if err != nil {
		return nil, err
	}
	if err := t.handleEvents(events); err != nil {
		return nil, err
	}
	if t.downgraded() {
		return nil, t.downgradeError()
	}
	if !t.completed {
		return nil, geminiapp.NewError("INCOMPLETE_NATIVE_RESPONSE", 502)
	}
	if !t.modeVerified() {
		return nil, geminiapp.NewError("MISSING_NATIVE_MODE_EVIDENCE", 502)
	}
	if t.parallelDisabled && len(t.calls) > 1 {
		return nil, geminiapp.NewError("PARALLEL_TOOL_CALLS_DISABLED", 502)
	}
	if err := geminiapp.ValidateToolCompletion(len(t.calls), t.policy); err != nil {
		return nil, err
	}
	content := t.content.String()
	if len(t.calls) == 0 && strings.TrimSpace(content) == "" {
		return nil, geminiapp.NewError("EMPTY_NATIVE_RESPONSE", 502)
	}
	return &geminiAppTurnResult{
		Content:  content,
		Calls:    t.calls,
		Frames:   t.frames,
		Response: t.responseID,
		Thinking: t.thinkingLevel,
		Mode:     t.observedMode,
	}, nil
}

func (t *geminiAppTurn) modeVerified() bool {
	return t.observedMode != nil && *t.observedMode == uint64(t.mode)
}

func (t *geminiAppTurn) downgraded() bool {
	if t.fallback {
		return true
	}
	if t.downgradeReason != nil && *t.downgradeReason != 0 {
		return true
	}
	return t.observedMode != nil && *t.observedMode != 0 && *t.observedMode != uint64(t.mode)
}

func (t *geminiAppTurn) downgradeError() error {
	// 2/3/6 = 配额耗尽，8 = 配额不足；其余为模型降级。
	if t.downgradeReason != nil {
		switch *t.downgradeReason {
		case 2, 3, 6:
			return geminiapp.NewError("NATIVE_QUOTA_EXHAUSTED", 429)
		case 8:
			return geminiapp.NewError("NATIVE_QUOTA_INSUFFICIENT", 429)
		}
	}
	return geminiapp.NewError("MODEL_DOWNGRADE_REJECTED", 502)
}

func (t *geminiAppTurn) firstTokenMs(start time.Time) *int {
	if t.firstContentAt.IsZero() {
		return nil
	}
	ms := int(t.firstContentAt.Sub(start).Milliseconds())
	return &ms
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func writeGeminiAppError(c *gin.Context, protocol geminiAppProtocol, status int, errType, message string) error {
	MarkResponseCommitted(c)
	if protocol == geminiAppProtocolResponses {
		c.JSON(status, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": message,
				"code":    nil,
				"param":   nil,
			},
		})
		return errors.New(message)
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	return errors.New(message)
}

// writeGeminiAppMappedError 把协议层错误映射为客户端错误响应。
func writeGeminiAppMappedError(c *gin.Context, protocol geminiAppProtocol, err error) error {
	status := http.StatusBadRequest
	code := "INVALID_REQUEST"
	if appErr, ok := geminiapp.AsError(err); ok {
		status = appErr.Status
		code = appErr.Code
	}
	errType := "invalid_request_error"
	if status >= http.StatusInternalServerError {
		errType = "upstream_error"
	}
	message := geminiAppErrorMessage(code)
	// 管理端配置的错误透传规则同样适用于 Gemini App：按上游状态码 +
	// 错误码关键字匹配，命中时改写状态码/类型/文案。
	if ptStatus, ptErrType, ptErrMsg, matched := applyErrorPassthroughRule(
		c, PlatformGeminiApp, status, []byte(code+": "+message), status, errType, message,
	); matched {
		if ptStatus >= 100 && ptStatus <= 599 {
			status = ptStatus
		}
		errType = ptErrType
		message = ptErrMsg
	}
	MarkResponseCommitted(c)
	if protocol == geminiAppProtocolResponses {
		c.JSON(status, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": message,
				"code":    code,
				"param":   nil,
			},
		})
		return fmt.Errorf("%s: %w", code, err)
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
			"code":    code,
		},
	})
	return fmt.Errorf("%s: %w", code, err)
}

func writeGeminiAppStreamError(c *gin.Context, protocol geminiAppProtocol, code, message string) {
	MarkResponseCommitted(c)
	if protocol == geminiAppProtocolResponses {
		c.Writer.WriteString("event: error\ndata: " + mustGeminiAppJSON(gin.H{
			"type":  "error",
			"error": gin.H{"type": "upstream_error", "code": code, "message": message},
		}) + "\n\n")
		return
	}
	c.Writer.WriteString("data: " + mustGeminiAppJSON(gin.H{
		"error": gin.H{"type": "upstream_error", "code": code, "message": message},
	}) + "\n\n")
}

func mustGeminiAppJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func geminiAppErrorMessage(code string) string {
	switch code {
	case "UNSUPPORTED_MODEL":
		return "model is not supported by Gemini App; use gemini-argon, gemini-hydrogen or gemini-flash-lite"
	case "UNSUPPORTED_IMAGE_INPUT":
		return "Gemini App does not accept image input"
	case "NATIVE_QUOTA_EXHAUSTED", "NATIVE_QUOTA_INSUFFICIENT":
		return "Gemini App quota exhausted"
	case "NATIVE_PERMISSION_OR_AUTH":
		return "Gemini App credential rejected by upstream"
	case "MODEL_DOWNGRADE_REJECTED":
		return "upstream served a different model than requested"
	case "EMPTY_NATIVE_RESPONSE":
		return "upstream returned an empty response"
	case "INCOMPLETE_NATIVE_RESPONSE":
		return "upstream response was incomplete"
	default:
		return "Gemini App request failed: " + code
	}
}

// geminiAppFailoverError 判断错误是否应切换账号。
func geminiAppFailoverError(err error) *UpstreamFailoverError {
	appErr, ok := geminiapp.AsError(err)
	if !ok {
		return nil
	}
	switch appErr.Status {
	case http.StatusTooManyRequests:
		return &UpstreamFailoverError{
			StatusCode:        http.StatusTooManyRequests,
			NextAccountAction: NextAccountRetry,
			ClientStatusCode:  http.StatusTooManyRequests,
			ClientMessage:     "Gemini App quota exhausted",
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &UpstreamFailoverError{
			StatusCode:        http.StatusBadGateway,
			Stage:             GatewayFailureStageAccountAuth,
			Scope:             GatewayFailureScopeAccount,
			Reason:            GeminiAppCredentialRejectedReason,
			NextAccountAction: NextAccountRetry,
			ClientStatusCode:  http.StatusBadGateway,
			ClientMessage:     GeminiAppCredentialRejectedClientMessage,
		}
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// 上游 5xx/超时是瞬时故障，换一个账号重试；仍失败时由外层返回原错误。
		return &UpstreamFailoverError{
			StatusCode:        appErr.Status,
			NextAccountAction: NextAccountRetry,
			ClientStatusCode:  http.StatusBadGateway,
			ClientMessage:     "Gemini App upstream error",
		}
	}
	return nil
}

func logGeminiAppFailure(account *Account, err error) {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	logger.LegacyPrintf("service.gemini_app", "native turn failed: account_id=%d error=%v", accountID, err)
}

// ---------------------------------------------------------------------------
// OpenAI gateway bridging
// ---------------------------------------------------------------------------

// forwardGeminiAppChatCompletions 处理经 OpenAI 网关入站的 Gemini App 请求。
func (s *OpenAIGatewayService) forwardGeminiAppChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	if s.geminiAppGatewayService == nil {
		MarkResponseCommitted(c)
		writeChatCompletionsError(c, http.StatusBadGateway, "upstream_error", "Gemini App service is not configured")
		return nil, errors.New("gemini app gateway service not configured")
	}
	SetActualOpenAIUpstreamEndpoint(c, GeminiAppUpstreamEndpoint)
	result, err := s.geminiAppGatewayService.ForwardAsChatCompletions(ctx, c, account, body)
	if err != nil {
		return nil, err
	}
	return geminiAppOpenAIForwardResult(result, geminiAppRequestedModel(account, body)), nil
}

// forwardGeminiAppResponses 处理经 OpenAI 网关入站的 Gemini App Responses 请求。
func (s *OpenAIGatewayService) forwardGeminiAppResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	if s.geminiAppGatewayService == nil {
		MarkResponseCommitted(c)
		writeResponsesError(c, http.StatusBadGateway, "upstream_error", "Gemini App service is not configured")
		return nil, errors.New("gemini app gateway service not configured")
	}
	SetActualOpenAIUpstreamEndpoint(c, GeminiAppUpstreamEndpoint)
	result, err := s.geminiAppGatewayService.ForwardAsResponses(ctx, c, account, body)
	if err != nil {
		return nil, err
	}
	return geminiAppOpenAIForwardResult(result, geminiAppRequestedModel(account, body)), nil
}

func geminiAppRequestedModel(account *Account, body []byte) string {
	var meta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return ""
	}
	model := strings.TrimSpace(meta.Model)
	if model != "" && account != nil {
		model = account.GetMappedModel(model)
	}
	return model
}

func geminiAppOpenAIForwardResult(result *ForwardResult, model string) *OpenAIForwardResult {
	if result == nil {
		return nil
	}
	if model == "" {
		model = result.Model
	}
	return &OpenAIForwardResult{
		RequestID:        result.RequestID,
		ResponseID:       result.RequestID,
		Usage:            OpenAIUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens},
		Model:            model,
		UpstreamModel:    result.UpstreamModel,
		Stream:           result.Stream,
		Duration:         result.Duration,
		FirstTokenMs:     result.FirstTokenMs,
		ClientDisconnect: result.ClientDisconnect,
		ReasoningEffort:  result.ReasoningEffort,
		UpstreamEndpoint: GeminiAppUpstreamEndpoint,
	}
}

// RunTestTurn 执行一次最小对话，返回文本内容（账号测试用）。
func (s *GeminiAppGatewayService) RunTestTurn(ctx context.Context, account *Account, model, prompt string) (string, error) {
	if s == nil || s.tokenProvider == nil {
		return "", errors.New("gemini app token provider not configured")
	}
	if account == nil || account.Platform != PlatformGeminiApp {
		return "", errors.New("not a gemini app account")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = geminiapp.ModelArgon
	}
	if !geminiapp.IsNativeModel(model) {
		return "", geminiapp.NewError("UNSUPPORTED_MODEL", http.StatusBadRequest)
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return "", err
	}
	client, err := s.httpClient(geminiAppProxyURL(ctx, account, s.proxyRepo))
	if err != nil {
		return "", err
	}
	canonical := &geminiapp.CanonicalRequest{
		Model:         model,
		ThinkingLevel: geminiapp.ThinkingNone,
		Messages:      []geminiapp.ChatMessage{{Role: "user", Content: prompt}},
	}
	built, err := geminiapp.BuildCanonicalPrompt(canonical)
	if err != nil {
		return "", err
	}
	turn := newGeminiAppTurn(model, geminiapp.ModelModes[model], geminiapp.ThinkingNone, nil, nil)
	result, err := turn.runWithResult(ctx, nativeTurnInput{
		accessToken: accessToken,
		prompt:      built.Text,
		model:       model,
		client:      client,
	})
	if err != nil {
		return "", err
	}
	return result.Content, nil
}
