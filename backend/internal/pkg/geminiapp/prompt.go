package geminiapp

import (
	"regexp"
	"strings"
)

// OpenAI-compatible request normalization and canonical prompt construction.
// Mirrors openai-contract.mjs and flattenMessages() in tool-protocol.mjs.

const (
	maxMessages    = 1024
	maxPromptBytes = 4 << 20
)

var toolCallIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// ToolCall is a normalized assistant tool request.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is a normalized function call.
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatMessage is one normalized conversation turn.
type ChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// CanonicalRequest is the normalized body understood by the native provider.
type CanonicalRequest struct {
	Model             string
	ThinkingLevel     int
	ReasoningEffort   string
	Stream            bool
	IncludeUsage      bool
	NativeSearch      bool
	ParallelToolCalls *bool

	Messages   []ChatMessage
	Tools      []any
	ToolChoice any
	Policy     *ToolPolicy

	IgnoredTokenParameters []string
}

var advisoryChatKeys = map[string]struct{}{
	"temperature": {}, "top_p": {}, "presence_penalty": {}, "frequency_penalty": {},
	"seed": {}, "logprobs": {}, "top_logprobs": {}, "response_format": {},
	"max_tokens": {}, "max_completion_tokens": {}, "stop": {}, "user": {}, "n": {},
}

var allowedChatKeys = map[string]struct{}{
	"model": {}, "messages": {}, "tools": {}, "tool_choice": {}, "thinking_level": {},
	"reasoning_effort": {}, "stream": {}, "stream_options": {}, "store": {},
	"parallel_tool_calls": {}, "reasoning": {}, "include": {}, "prompt_cache_key": {},
	"client_metadata": {}, "native_search": {},
}

// NormalizeChatRequest validates and normalizes a Chat Completions body.
func NormalizeChatRequest(payload map[string]any) (*CanonicalRequest, error) {
	if err := checkChatKeys(payload); err != nil {
		return nil, err
	}
	req := &CanonicalRequest{Model: ModelArgon, ThinkingLevel: ThinkingExtended}
	if model, exists := payload["model"]; exists {
		value, ok := model.(string)
		if !ok {
			return nil, NewError("UNSUPPORTED_MODEL", 400)
		}
		value = strings.TrimPrefix(value, "models/")
		if !IsNativeModel(value) {
			return nil, NewError("UNSUPPORTED_MODEL", 400)
		}
		req.Model = value
	}
	level, effort, err := thinkingSettings(payload)
	if err != nil {
		return nil, err
	}
	req.ThinkingLevel = level
	req.ReasoningEffort = effort

	if stream, exists := payload["stream"]; exists {
		value, ok := stream.(bool)
		if !ok {
			return nil, NewError("INVALID_STREAM", 400)
		}
		req.Stream = value
	}
	if nativeSearch, exists := payload["native_search"]; exists {
		value, ok := nativeSearch.(bool)
		if !ok {
			return nil, NewError("INVALID_NATIVE_SEARCH", 400)
		}
		req.NativeSearch = value
	}
	if parallel, exists := payload["parallel_tool_calls"]; exists {
		value, ok := parallel.(bool)
		if !ok {
			return nil, NewError("INVALID_PARALLEL_TOOL_CALLS", 400)
		}
		req.ParallelToolCalls = &value
	}
	if store, exists := payload["store"]; exists {
		if value, ok := store.(bool); !ok || value {
			return nil, NewError("UNSUPPORTED_STORE", 400)
		}
	}
	if options, exists := payload["stream_options"]; exists {
		optionsMap, ok := options.(map[string]any)
		if !ok {
			return nil, NewError("INVALID_STREAM_OPTIONS", 400)
		}
		if err := onlyKeys(optionsMap, "include_usage"); err != nil {
			return nil, NewError("INVALID_STREAM_OPTIONS", 400)
		}
		if include, exists := optionsMap["include_usage"]; exists {
			value, ok := include.(bool)
			if !ok || !req.Stream {
				return nil, NewError("INVALID_STREAM_OPTIONS", 400)
			}
			req.IncludeUsage = value
		}
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if _, exists := payload[key]; exists {
			req.IgnoredTokenParameters = append(req.IgnoredTokenParameters, key)
		}
	}

	rawTools := []any{}
	if tools, exists := payload["tools"]; exists {
		list, ok := tools.([]any)
		if !ok {
			return nil, NewError("INVALID_TOOLS", 400)
		}
		rawTools = stripToolAnnotations(list)
	}
	choice := any("auto")
	if declared, exists := payload["tool_choice"]; exists {
		choice = declared
	}
	policy, err := CompileToolPolicy(rawTools, choice)
	if err != nil {
		return nil, err
	}
	req.Tools = rawTools
	req.ToolChoice = choice
	req.Policy = policy
	if req.NativeSearch {
		if req.Model != ModelArgon || req.ThinkingLevel != ThinkingExtended {
			return nil, NewError("UNSUPPORTED_NATIVE_SEARCH_MODE", 400)
		}
		if len(rawTools) > 0 {
			return nil, NewError("NATIVE_SEARCH_TOOL_CONFLICT", 400)
		}
		if req.ParallelToolCalls != nil {
			return nil, NewError("NATIVE_SEARCH_TOOL_CONFLICT", 400)
		}
	}

	messages, exists := payload["messages"]
	if !exists {
		return nil, NewError("MESSAGES_REQUIRED", 400)
	}
	list, ok := messages.([]any)
	if !ok || len(list) == 0 || len(list) > maxMessages {
		return nil, NewError("MESSAGES_REQUIRED", 400)
	}
	normalized, err := normalizeMessages(list)
	if err != nil {
		return nil, err
	}
	req.Messages = normalized
	if _, err := BuildCanonicalPrompt(req); err != nil {
		return nil, err
	}
	return req, nil
}

func checkChatKeys(payload map[string]any) error {
	for key := range payload {
		if _, ok := allowedChatKeys[key]; ok {
			continue
		}
		if _, ok := advisoryChatKeys[key]; ok {
			continue
		}
		return NewErrorf("UNSUPPORTED_PARAMETER", 400, "%s", key)
	}
	return nil
}

func thinkingSettings(payload map[string]any) (int, string, error) {
	levels := make([]int, 0, 2)
	effortValue := ""
	for _, source := range []string{"reasoning_effort"} {
		if raw, exists := payload[source]; exists {
			effort, ok := raw.(string)
			if !ok {
				return 0, "", NewError("INVALID_REASONING_EFFORT", 400)
			}
			level, ok := ReasoningEffortLevel[effort]
			if !ok {
				return 0, "", NewError("INVALID_REASONING_EFFORT", 400)
			}
			levels = append(levels, level)
			effortValue = effort
		}
	}
	if reasoning, exists := payload["reasoning"]; exists {
		reasoningMap, ok := reasoning.(map[string]any)
		if !ok {
			return 0, "", NewError("UNSUPPORTED_REASONING", 400)
		}
		if err := onlyKeys(reasoningMap, "summary", "effort"); err != nil {
			return 0, "", NewError("UNSUPPORTED_REASONING", 400)
		}
		if summary, exists := reasoningMap["summary"]; exists {
			if value, ok := summary.(string); !ok || value != "none" {
				return 0, "", NewError("UNSUPPORTED_REASONING", 400)
			}
		}
		if raw, exists := reasoningMap["effort"]; exists {
			effort, ok := raw.(string)
			if !ok {
				return 0, "", NewError("INVALID_REASONING_EFFORT", 400)
			}
			level, ok := ReasoningEffortLevel[effort]
			if !ok {
				return 0, "", NewError("INVALID_REASONING_EFFORT", 400)
			}
			levels = append(levels, level)
			if effortValue == "" {
				effortValue = effort
			}
		}
	}
	if raw, exists := payload["thinking_level"]; exists {
		number, ok := raw.(float64)
		if !ok || number != float64(int(number)) || number < 0 || number > ThinkingMax {
			return 0, "", NewError("INVALID_THINKING_LEVEL", 400)
		}
		levels = append(levels, int(number))
	}
	for _, level := range levels {
		if level != levels[0] {
			return 0, "", NewError("CONFLICTING_THINKING_SETTINGS", 400)
		}
	}
	level := ThinkingExtended
	if len(levels) > 0 {
		level = levels[0]
	}
	if level == ThinkingDeep {
		return 0, "", NewError("NATIVE_DEEP_THINK_UNSUPPORTED", 422)
	}
	return level, effortValue, nil
}

func normalizeMessages(list []any) ([]ChatMessage, error) {
	out := make([]ChatMessage, 0, len(list))
	pending := map[string]struct{}{}
	seen := map[string]struct{}{}
	for _, raw := range list {
		message, ok := raw.(map[string]any)
		if !ok {
			return nil, NewError("INVALID_MESSAGE", 400)
		}
		if err := onlyKeys(message, "role", "content", "tool_calls", "tool_call_id", "refusal", "parsed"); err != nil {
			return nil, NewError("UNSUPPORTED_PARAMETER", 400)
		}
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil, NewError("UNSUPPORTED_ROLE", 400)
		}
		if len(pending) > 0 && role != "tool" {
			return nil, NewError("PENDING_TOOL_CALLS", 400)
		}
		if role != "assistant" {
			if _, exists := message["tool_calls"]; exists {
				return nil, NewError("INVALID_TOOL_CALL", 400)
			}
		}
		if role != "tool" {
			if _, exists := message["tool_call_id"]; exists {
				return nil, NewError("INVALID_TOOL_RESULT", 400)
			}
		}
		for _, key := range []string{"refusal", "parsed"} {
			if value, exists := message[key]; exists && (role != "assistant" || value != nil) {
				return nil, NewError("UNSUPPORTED_PARAMETER", 400)
			}
		}
		content, err := contentText(message["content"], role)
		if err != nil {
			return nil, err
		}
		normalized := ChatMessage{Role: role, Content: content}
		if calls, exists := message["tool_calls"]; exists {
			decoded, err := normalizeToolCalls(calls, seen)
			if err != nil {
				return nil, err
			}
			normalized.ToolCalls = decoded
			for _, call := range decoded {
				pending[call.ID] = struct{}{}
			}
		}
		if role == "tool" {
			callID, _ := message["tool_call_id"].(string)
			if !toolCallIDPattern.MatchString(callID) {
				return nil, NewError("INVALID_TOOL_RESULT", 400)
			}
			if _, ok := pending[callID]; !ok {
				return nil, NewError("INVALID_TOOL_RESULT", 400)
			}
			delete(pending, callID)
			normalized.ToolCallID = callID
		}
		out = append(out, normalized)
	}
	if len(pending) > 0 {
		return nil, NewError("PENDING_TOOL_CALLS", 400)
	}
	return out, nil
}

func contentText(raw any, role string) (string, error) {
	if raw == nil {
		if role == "assistant" {
			return "", nil
		}
		return "", NewError("INVALID_MESSAGE_CONTENT", 400)
	}
	if text, ok := raw.(string); ok {
		return text, nil
	}
	parts, ok := raw.([]any)
	if !ok || len(parts) == 0 || len(parts) > 1024 {
		return "", NewError("INVALID_MESSAGE_CONTENT", 400)
	}
	var builder strings.Builder
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return "", NewError("INVALID_MESSAGE_CONTENT", 400)
		}
		partType, _ := part["type"].(string)
		if partType == "image_url" || partType == "input_image" {
			return "", NewError("UNSUPPORTED_IMAGE_INPUT", 400)
		}
		if partType != "text" && partType != "input_text" && partType != "output_text" {
			return "", NewError("UNSUPPORTED_CONTENT_TYPE", 400)
		}
		text, ok := part["text"].(string)
		if !ok {
			return "", NewError("INVALID_MESSAGE_CONTENT", 400)
		}
		builder.WriteString(text)
	}
	return builder.String(), nil
}

func normalizeToolCalls(raw any, seen map[string]struct{}) ([]ToolCall, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > maxToolCalls {
		return nil, NewError("INVALID_TOOL_CALL", 400)
	}
	out := make([]ToolCall, 0, len(list))
	for _, rawCall := range list {
		call, ok := rawCall.(map[string]any)
		if !ok {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		if err := onlyKeys(call, "id", "type", "function"); err != nil {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		id, _ := call["id"].(string)
		if !toolCallIDPattern.MatchString(id) {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		seen[id] = struct{}{}
		callType, _ := call["type"].(string)
		fn, ok := call["function"].(map[string]any)
		if callType != "function" || !ok {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		if err := onlyKeys(fn, "name", "arguments"); err != nil {
			return nil, NewError("INVALID_TOOL_CALL", 400)
		}
		name, _ := fn["name"].(string)
		arguments, _ := fn["arguments"].(string)
		parsed, err := ParseBoundedJSON(arguments, 400)
		if err != nil {
			return nil, NewError("INVALID_TOOL_JSON", 400)
		}
		if _, ok := parsed.(map[string]any); !ok {
			return nil, NewError("INVALID_TOOL_ARGUMENTS", 400)
		}
		canonical, _ := marshalJSON(parsed)
		out = append(out, ToolCall{
			ID:   id,
			Type: "function",
			Function: ToolFunction{
				Name:      name,
				Arguments: canonical,
			},
		})
	}
	return out, nil
}

func stripToolAnnotations(tools []any) []any {
	out := make([]any, len(tools))
	for i, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		clone := make(map[string]any, len(tool))
		for key, value := range tool {
			clone[key] = value
		}
		fnClone := make(map[string]any, len(fn))
		for key, value := range fn {
			fnClone[key] = value
		}
		if parameters, ok := fnClone["parameters"]; ok {
			fnClone["parameters"] = stripSchemaAnnotations(parameters)
		}
		clone["function"] = fnClone
		out[i] = clone
	}
	return out
}

var schemaAnnotationKeys = map[string]struct{}{
	"$schema": {}, "$id": {}, "title": {}, "default": {}, "examples": {},
	"deprecated": {}, "readOnly": {}, "writeOnly": {}, "format": {},
}

func stripSchemaAnnotations(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		if list, ok := value.([]any); ok {
			out := make([]any, len(list))
			for i, child := range list {
				out[i] = stripSchemaAnnotations(child)
			}
			return out
		}
		return value
	}
	out := make(map[string]any, len(object))
	for key, child := range object {
		if _, annotation := schemaAnnotationKeys[key]; annotation {
			continue
		}
		out[key] = stripSchemaAnnotations(child)
	}
	return out
}

// Prompt is the exact text sent to the native API.
type Prompt struct {
	Text      string
	ToolCount int
}

// BuildCanonicalPrompt renders the canonical prompt for req.
func BuildCanonicalPrompt(req *CanonicalRequest) (*Prompt, error) {
	systemParts := make([]string, 0, 2)
	transcript := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch message.Role {
		case "system", "developer":
			systemParts = append(systemParts, message.Content)
		case "user":
			transcript = append(transcript, "[User]\n"+message.Content)
		case "assistant":
			entry := "[Assistant]\n" + message.Content
			if len(message.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					arguments, err := ParseBoundedJSON(call.Function.Arguments, 400)
					if err != nil {
						return nil, NewError("INVALID_TOOL_ARGUMENTS", 400)
					}
					calls = append(calls, map[string]any{
						"id":        call.ID,
						"name":      call.Function.Name,
						"arguments": arguments,
					})
				}
				encoded, _ := marshalJSON(calls)
				entry += "\n[Prior tool requests; not execution proof]\n" + encoded
			}
			transcript = append(transcript, entry)
		case "tool":
			encoded, _ := marshalJSON(map[string]any{
				"tool_call_id": message.ToolCallID,
				"content":      message.Content,
			})
			transcript = append(transcript, "[Caller tool result]\n"+encoded)
		}
	}
	if len(transcript) == 0 {
		return nil, NewError("NON_SYSTEM_MESSAGE_REQUIRED", 400)
	}
	instruction, _, err := BuildToolInstruction(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	systemText := strings.Join(systemParts, "\n\n")
	userText := "[Conversation]\n" + strings.Join(transcript, "\n\n")
	preamble := strings.Join(nonEmpty(instruction, systemText), "\n\n")
	text := "[System Instructions]\n" + preamble + "\n\n" + userText
	if len(text) > maxPromptBytes {
		return nil, NewError("PROMPT_TOO_LARGE", 413)
	}
	return &Prompt{Text: text, ToolCount: len(req.Tools)}, nil
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

var responsesOnlyKeys = map[string]struct{}{
	"input": {}, "instructions": {}, "max_output_tokens": {}, "text": {},
	"include": {},
}

// responsesTextValue flattens a Responses API text value: a plain string, or an
// array of input_text/text content parts (the shape function_call_output uses).
// Anything else is rejected instead of being silently coerced to "".
func responsesTextValue(raw any) (string, error) {
	switch value := raw.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []any:
		if len(value) > 1024 {
			return "", NewError("INVALID_INPUT_ITEM", 400)
		}
		var builder strings.Builder
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return "", NewError("INVALID_INPUT_ITEM", 400)
			}
			partType, _ := part["type"].(string)
			if partType != "input_text" && partType != "text" && partType != "output_text" {
				return "", NewError("INVALID_INPUT_ITEM", 400)
			}
			text, ok := part["text"].(string)
			if !ok {
				return "", NewError("INVALID_INPUT_ITEM", 400)
			}
			builder.WriteString(text)
		}
		return builder.String(), nil
	default:
		return "", NewError("INVALID_INPUT_ITEM", 400)
	}
}

var allowedResponsesKeys = map[string]struct{}{
	"model": {}, "input": {}, "instructions": {}, "tools": {}, "tool_choice": {},
	"thinking_level": {}, "reasoning_effort": {}, "stream": {}, "store": {},
	"parallel_tool_calls": {}, "reasoning": {}, "include": {}, "prompt_cache_key": {},
	"client_metadata": {}, "native_search": {}, "temperature": {}, "top_p": {},
	"max_output_tokens": {}, "text": {},
}

// NormalizeResponsesRequest converts a Responses API body into the canonical
// chat request understood by the native provider.
func NormalizeResponsesRequest(payload map[string]any) (*CanonicalRequest, error) {
	for key := range payload {
		if _, ok := allowedResponsesKeys[key]; ok {
			continue
		}
		return nil, NewErrorf("UNSUPPORTED_PARAMETER", 400, "%s", key)
	}
	if include, exists := payload["include"]; exists {
		list, ok := include.([]any)
		if !ok {
			return nil, NewError("UNSUPPORTED_INCLUDE", 400)
		}
		for _, item := range list {
			if value, ok := item.(string); !ok || value != "reasoning.encrypted_content" {
				return nil, NewError("UNSUPPORTED_INCLUDE", 400)
			}
		}
	}

	messages := make([]any, 0, 4)
	if instructions, exists := payload["instructions"]; exists && instructions != nil {
		text, ok := instructions.(string)
		if !ok {
			return nil, NewError("INVALID_REQUEST", 400)
		}
		messages = append(messages, map[string]any{"role": "system", "content": text})
	}

	var input []any
	switch value := payload["input"].(type) {
	case string:
		input = []any{map[string]any{"role": "user", "content": value}}
	case []any:
		input = value
	default:
		return nil, NewError("INPUT_REQUIRED", 400)
	}
	if len(input) == 0 || len(input) > maxMessages {
		return nil, NewError("INPUT_REQUIRED", 400)
	}

	tools, choice, err := normalizeResponsesTools(payload["tools"], payload["tool_choice"])
	if err != nil {
		return nil, err
	}

	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, NewError("INVALID_INPUT_ITEM", 400)
		}
		itemType, _ := item["type"].(string)
		switch itemType {
		case "function_call":
			if err := onlyKeys(item, "type", "id", "status", "call_id", "namespace", "name", "arguments", "parsed_arguments"); err != nil {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			if namespace, _ := item["namespace"].(string); namespace != "" {
				return nil, NewError("UNSUPPORTED_TOOL_TYPE", 400)
			}
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			if !toolCallIDPattern.MatchString(callID) || name == "" {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			call := map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			}
			if len(messages) > 0 {
				if previous, ok := messages[len(messages)-1].(map[string]any); ok && previous["role"] == "assistant" {
					existing, _ := previous["tool_calls"].([]any)
					previous["tool_calls"] = append(existing, call)
					continue
				}
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{call}})
		case "function_call_output":
			if err := onlyKeys(item, "type", "id", "call_id", "output"); err != nil {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			callID, _ := item["call_id"].(string)
			output, err := responsesTextValue(item["output"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": output})
		case "input_text":
			if err := onlyKeys(item, "type", "text"); err != nil {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			text, ok := item["text"].(string)
			if !ok {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			messages = append(messages, map[string]any{"role": "user", "content": text})
		case "input_image":
			return nil, NewError("UNSUPPORTED_IMAGE_INPUT", 400)
		default:
			if itemType != "" && itemType != "message" {
				return nil, NewError("UNSUPPORTED_INPUT_TYPE", 400)
			}
			if err := onlyKeys(item, "type", "id", "status", "role", "content"); err != nil {
				return nil, NewError("INVALID_INPUT_ITEM", 400)
			}
			role, _ := item["role"].(string)
			messages = append(messages, map[string]any{"role": role, "content": item["content"]})
		}
	}

	chatPayload := map[string]any{"messages": messages}
	for key, value := range payload {
		if _, skip := responsesOnlyKeys[key]; skip {
			continue
		}
		chatPayload[key] = value
	}
	if len(tools) > 0 {
		chatPayload["tools"] = tools
		chatPayload["tool_choice"] = choice
	} else {
		delete(chatPayload, "tools")
		// 客户端显式给了 tool_choice（如 "required" 或指定函数）却没有声明任何
		// 工具时，不能静默丢弃：交给 NormalizeChatRequest 按 Chat 路径同样的规则
		// 报 TOOLS_REQUIRED / UNDECLARED_TOOL，而不是悄悄降级成 auto。
		if rawChoice, declared := payload["tool_choice"]; declared && rawChoice != nil {
			chatPayload["tool_choice"] = choice
		} else {
			delete(chatPayload, "tool_choice")
		}
	}
	req, err := NormalizeChatRequest(chatPayload)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func normalizeResponsesTools(rawTools, rawChoice any) ([]any, any, error) {
	tools := []any{}
	if rawTools != nil {
		list, ok := rawTools.([]any)
		if !ok {
			return nil, nil, NewError("INVALID_TOOLS", 400)
		}
		for _, raw := range list {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, NewError("INVALID_TOOL_DECLARATION", 400)
			}
			toolType, _ := tool["type"].(string)
			if toolType != "function" {
				return nil, nil, NewError("UNSUPPORTED_TOOL_TYPE", 400)
			}
			// Responses declares functions at the top level rather than nesting
			// them under "function".
			if _, nested := tool["function"]; !nested {
				fn := map[string]any{}
				for key, value := range tool {
					if key == "type" {
						continue
					}
					fn[key] = value
				}
				tool = map[string]any{"type": "function", "function": fn}
			}
			tools = append(tools, tool)
		}
	}

	choice := any("auto")
	switch value := rawChoice.(type) {
	case nil:
	case string:
		choice = value
	case map[string]any:
		if value["type"] != "function" {
			return nil, nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		name, _ := value["name"].(string)
		if name == "" {
			if fn, ok := value["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
		}
		if name == "" {
			return nil, nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		choice = map[string]any{"type": "function", "function": map[string]any{"name": name}}
	default:
		return nil, nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
	}
	return tools, choice, nil
}
