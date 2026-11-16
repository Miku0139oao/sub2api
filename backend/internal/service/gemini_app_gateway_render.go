package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	"github.com/gin-gonic/gin"
)

// geminiAppRenderer 把原生转换结果渲染成客户端协议（Chat Completions / Responses）。
type geminiAppRenderer struct {
	protocol     geminiAppProtocol
	c            *gin.Context
	model        string
	id           string
	created      int64
	stream       bool
	includeUsage bool

	sse          *geminiAppSSEWriter
	turn         *geminiAppTurn
	streamBegun  bool
	roleSent     bool
	toolIndex    int
	sequence     int
	outputIndex  int
	textLen      int
	messageID    string
	messageOpen  bool
	responsesOut []apicompat.ResponsesOutput
}

func newGeminiAppRenderer(
	protocol geminiAppProtocol,
	c *gin.Context,
	canonical *geminiapp.CanonicalRequest,
	startTime time.Time,
	responseID string,
) *geminiAppRenderer {
	_ = startTime
	return &geminiAppRenderer{
		protocol: protocol,
		c:        c,
		model:    canonical.Model,
		id:       responseID,
		created:  time.Now().Unix(),
	}
}

// beginStream 设置 SSE 响应头；不写入任何字节，以便失败时仍可切换账号。
func (r *geminiAppRenderer) beginStream() error {
	flusher, ok := r.c.Writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}
	r.c.Header("Content-Type", "text/event-stream")
	r.c.Header("Cache-Control", "no-cache")
	r.c.Header("Connection", "keep-alive")
	r.c.Header("X-Accel-Buffering", "no")
	r.c.Status(http.StatusOK)
	r.sse = &geminiAppSSEWriter{writer: r.c.Writer, flusher: flusher}
	r.streamBegun = true
	return nil
}

func (r *geminiAppRenderer) disconnected() bool {
	return r.sse != nil && r.sse.disconnected
}

// emit 由 turn 在内容/工具调用确定后回调。
func (r *geminiAppRenderer) emit(event geminiapp.ToolEvent) error {
	if !r.stream {
		return nil
	}
	switch event.Type {
	case "content":
		switch r.protocol {
		case geminiAppProtocolResponses:
			r.ensureResponsesStarted()
			r.textLen += len(event.Text)
			r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
				Type:         "response.output_text.delta",
				ItemID:       r.messageID,
				OutputIndex:  r.outputIndex,
				ContentIndex: 0,
				Delta:        event.Text,
			})
		default:
			r.ensureChatRole()
			r.writeChatChunk(gin.H{
				"choices": []gin.H{{
					"index":         0,
					"delta":         gin.H{"content": event.Text},
					"finish_reason": nil,
				}},
			})
		}
	case "call":
		if event.Call == nil {
			return nil
		}
		switch r.protocol {
		case geminiAppProtocolResponses:
			r.emitResponsesCall(event.Call)
		default:
			r.ensureChatRole()
			r.writeChatChunk(gin.H{
				"choices": []gin.H{{
					"index": 0,
					"delta": gin.H{
						"tool_calls": []gin.H{{
							"index": r.toolIndex,
							"id":    event.Call.ID,
							"type":  "function",
							"function": gin.H{
								"name":      event.Call.Function.Name,
								"arguments": event.Call.Function.Arguments,
							},
						}},
					},
					"finish_reason": nil,
				}},
			})
			r.toolIndex++
		}
	}
	return nil
}

// finish 输出终止事件并构造计费结果。
func (r *geminiAppRenderer) finish(result *geminiAppTurnResult, promptText string) (*ForwardResult, error) {
	usage := geminiAppEstimateUsage(promptText, result)
	finishReason := "stop"
	if len(result.Calls) > 0 {
		finishReason = "tool_calls"
	}

	if !r.stream {
		MarkResponseCommitted(r.c)
		switch r.protocol {
		case geminiAppProtocolResponses:
			r.c.JSON(http.StatusOK, r.buildResponsesResponse(result, usage))
		default:
			r.c.JSON(http.StatusOK, r.buildChatResponse(result, finishReason, usage))
		}
		return &ForwardResult{
			Usage: usage,
			Model: r.model,
		}, nil
	}

	switch r.protocol {
	case geminiAppProtocolResponses:
		r.finishResponsesStream(result, usage)
	default:
		r.finishChatStream(result, finishReason, usage)
	}
	return &ForwardResult{
		Usage: usage,
		Model: r.model,
	}, nil
}

// fail 把上游/协议错误转成客户端可见错误；未写出字节时保留 failover 能力。
func (r *geminiAppRenderer) fail(err error) error {
	logGeminiAppFailure(nil, err)
	if failover := geminiAppFailoverError(err); failover != nil && (r.sse == nil || !r.sse.wrote) {
		return failover
	}
	code := "NATIVE_TURN_FAILED"
	status := http.StatusBadGateway
	if appErr, ok := geminiapp.AsError(err); ok {
		code = appErr.Code
		status = appErr.Status
	}
	if r.stream {
		writeGeminiAppStreamError(r.c, r.protocol, code, geminiAppErrorMessage(code))
		return err
	}
	_ = status
	return writeGeminiAppMappedError(r.c, r.protocol, err)
}

// ---------------------------------------------------------------------------
// Chat Completions rendering
// ---------------------------------------------------------------------------

func (r *geminiAppRenderer) ensureChatRole() {
	if r.roleSent {
		return
	}
	r.roleSent = true
	r.writeChatChunk(gin.H{
		"choices": []gin.H{{
			"index":         0,
			"delta":         gin.H{"role": "assistant"},
			"finish_reason": nil,
		}},
	})
}

func (r *geminiAppRenderer) writeChatChunk(body gin.H) {
	body["id"] = r.id
	body["object"] = "chat.completion.chunk"
	body["created"] = r.created
	body["model"] = r.model
	r.sse.write("data: " + mustGeminiAppJSON(body) + "\n\n")
}

func (r *geminiAppRenderer) buildChatResponse(
	result *geminiAppTurnResult,
	finishReason string,
	usage ClaudeUsage,
) gin.H {
	message := gin.H{"role": "assistant"}
	if result.Content != "" {
		message["content"] = result.Content
	} else {
		message["content"] = nil
	}
	if len(result.Calls) > 0 {
		calls := make([]gin.H, 0, len(result.Calls))
		for index, call := range result.Calls {
			calls = append(calls, gin.H{
				"index": index,
				"id":    call.ID,
				"type":  "function",
				"function": gin.H{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		message["tool_calls"] = calls
	}
	return gin.H{
		"id":      r.id,
		"object":  "chat.completion",
		"created": r.created,
		"model":   r.model,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": gin.H{
			"prompt_tokens":     usage.InputTokens,
			"completion_tokens": usage.OutputTokens,
			"total_tokens":      usage.InputTokens + usage.OutputTokens,
		},
	}
}

func (r *geminiAppRenderer) finishChatStream(
	result *geminiAppTurnResult,
	finishReason string,
	usage ClaudeUsage,
) {
	r.ensureChatRole()
	r.writeChatChunk(gin.H{
		"choices": []gin.H{{
			"index":         0,
			"delta":         gin.H{},
			"finish_reason": finishReason,
		}},
	})
	if r.includeUsage {
		r.sse.write("data: " + mustGeminiAppJSON(gin.H{
			"id":      r.id,
			"object":  "chat.completion.chunk",
			"created": r.created,
			"model":   r.model,
			"choices": []gin.H{},
			"usage": gin.H{
				"prompt_tokens":     usage.InputTokens,
				"completion_tokens": usage.OutputTokens,
				"total_tokens":      usage.InputTokens + usage.OutputTokens,
			},
		}) + "\n\n")
	}
	MarkResponseCommitted(r.c)
	r.sse.write("data: [DONE]\n\n")
	_ = result
}

// ---------------------------------------------------------------------------
// Responses rendering
// ---------------------------------------------------------------------------

func (r *geminiAppRenderer) buildResponsesResponse(result *geminiAppTurnResult, usage ClaudeUsage) *apicompat.ResponsesResponse {
	outputs := make([]apicompat.ResponsesOutput, 0, 2)
	if result.Content != "" {
		item := apicompat.ResponsesOutput{
			Type:    "message",
			ID:      r.newItemID(),
			Role:    "assistant",
			Status:  "completed",
			Content: []apicompat.ResponsesContentPart{{Type: "output_text", Text: result.Content}},
		}
		outputs = append(outputs, item)
	}
	for _, call := range result.Calls {
		outputs = append(outputs, apicompat.ResponsesOutput{
			Type:      "function_call",
			ID:        r.newItemID(),
			CallID:    call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
			Status:    "completed",
		})
	}
	if len(outputs) == 0 {
		outputs = append(outputs, apicompat.ResponsesOutput{
			Type:    "message",
			ID:      r.newItemID(),
			Role:    "assistant",
			Status:  "completed",
			Content: []apicompat.ResponsesContentPart{{Type: "output_text", Text: ""}},
		})
	}
	return &apicompat.ResponsesResponse{
		ID:        r.id,
		Object:    "response",
		CreatedAt: r.created,
		Model:     r.model,
		Status:    "completed",
		Output:    outputs,
		Usage:     geminiAppResponsesUsage(usage),
	}
}

// ensureResponsesStarted 惰性发送 response.created 与 message item 开启事件。
func (r *geminiAppRenderer) ensureResponsesStarted() {
	if r.sequence > 0 {
		return
	}
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type: "response.created",
		Response: &apicompat.ResponsesResponse{
			ID:        r.id,
			Object:    "response",
			CreatedAt: r.created,
			Model:     r.model,
			Status:    "in_progress",
			Output:    []apicompat.ResponsesOutput{},
		},
	})
	r.messageID = r.newItemID()
	r.messageOpen = true
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: r.outputIndex,
		Item: &apicompat.ResponsesOutput{
			Type:   "message",
			ID:     r.messageID,
			Role:   "assistant",
			Status: "in_progress",
		},
	})
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:         "response.content_part.added",
		ItemID:       r.messageID,
		OutputIndex:  r.outputIndex,
		ContentIndex: 0,
		Part:         &apicompat.ResponsesContentPart{Type: "output_text", Text: ""},
	})
}

func (r *geminiAppRenderer) closeResponsesMessage(content string) {
	if !r.messageOpen {
		return
	}
	r.messageOpen = false
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:         "response.output_text.done",
		ItemID:       r.messageID,
		OutputIndex:  r.outputIndex,
		ContentIndex: 0,
		Text:         content,
	})
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:         "response.content_part.done",
		ItemID:       r.messageID,
		OutputIndex:  r.outputIndex,
		ContentIndex: 0,
		Part:         &apicompat.ResponsesContentPart{Type: "output_text", Text: content},
	})
	item := apicompat.ResponsesOutput{
		Type:    "message",
		ID:      r.messageID,
		Role:    "assistant",
		Status:  "completed",
		Content: []apicompat.ResponsesContentPart{{Type: "output_text", Text: content}},
	}
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: r.outputIndex,
		Item:        &item,
	})
	r.responsesOut = append(r.responsesOut, item)
	r.outputIndex++
}

func (r *geminiAppRenderer) emitResponsesCall(call *geminiapp.ToolCall) {
	r.ensureResponsesStarted()
	r.closeResponsesMessage(r.turnContent())
	itemID := r.newItemID()
	item := apicompat.ResponsesOutput{
		Type:      "function_call",
		ID:        itemID,
		CallID:    call.ID,
		Name:      call.Function.Name,
		Arguments: "",
		Status:    "in_progress",
	}
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: r.outputIndex,
		Item:        &item,
	})
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		ItemID:      itemID,
		OutputIndex: r.outputIndex,
		CallID:      call.ID,
		Name:        call.Function.Name,
		Delta:       call.Function.Arguments,
	})
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		ItemID:      itemID,
		OutputIndex: r.outputIndex,
		CallID:      call.ID,
		Name:        call.Function.Name,
		Arguments:   call.Function.Arguments,
	})
	item.Arguments = call.Function.Arguments
	item.Status = "completed"
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: r.outputIndex,
		Item:        &item,
	})
	r.responsesOut = append(r.responsesOut, item)
	r.outputIndex++
}

func (r *geminiAppRenderer) turnContent() string {
	if r.turn == nil {
		return ""
	}
	return r.turn.content.String()
}

func (r *geminiAppRenderer) finishResponsesStream(result *geminiAppTurnResult, usage ClaudeUsage) {
	r.ensureResponsesStarted()
	r.closeResponsesMessage(result.Content)
	outputs := append([]apicompat.ResponsesOutput{}, r.responsesOut...)
	if len(outputs) == 0 {
		outputs = r.buildResponsesResponse(result, usage).Output
	}
	MarkResponseCommitted(r.c)
	r.writeResponsesEvent(apicompat.ResponsesStreamEvent{
		Type: "response.completed",
		Response: &apicompat.ResponsesResponse{
			ID:        r.id,
			Object:    "response",
			CreatedAt: r.created,
			Model:     r.model,
			Status:    "completed",
			Output:    outputs,
			Usage:     geminiAppResponsesUsage(usage),
		},
	})
}

func (r *geminiAppRenderer) writeResponsesEvent(event apicompat.ResponsesStreamEvent) {
	event.SequenceNumber = r.sequence
	r.sequence++
	data, err := apicompat.ResponsesEventToSSE(event)
	if err != nil {
		return
	}
	r.sse.write(data)
}

func (r *geminiAppRenderer) newItemID() string {
	uuid, err := geminiapp.NewUUID()
	if err != nil {
		return fmt.Sprintf("item_%d", time.Now().UnixNano())
	}
	return "item_" + strings.ReplaceAll(uuid, "-", "")
}

func geminiAppResponsesUsage(usage ClaudeUsage) *apicompat.ResponsesUsage {
	return &apicompat.ResponsesUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.InputTokens + usage.OutputTokens,
	}
}

func geminiAppEstimateUsage(promptText string, result *geminiAppTurnResult) ClaudeUsage {
	input := estimateTokensForText(promptText)
	if input < 1 {
		input = 1
	}
	output := 0
	if result != nil {
		output += estimateTokensForText(result.Content)
		for _, call := range result.Calls {
			output += estimateTokensForText(call.Function.Name) + estimateTokensForText(call.Function.Arguments) + 4
		}
	}
	if output < 1 {
		output = 1
	}
	return ClaudeUsage{InputTokens: input, OutputTokens: output}
}

// ---------------------------------------------------------------------------
// SSE writer
// ---------------------------------------------------------------------------

type geminiAppSSEWriter struct {
	writer       gin.ResponseWriter
	flusher      http.Flusher
	wrote        bool
	disconnected bool
}

func (w *geminiAppSSEWriter) write(data string) {
	if w.disconnected || data == "" {
		return
	}
	if _, err := w.writer.WriteString(data); err != nil {
		w.disconnected = true
		return
	}
	w.wrote = true
	w.flusher.Flush()
}
