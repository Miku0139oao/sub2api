//go:build unit

package geminiapp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBoundedJSON(t *testing.T) {
	value, err := ParseBoundedJSON(`{"a":1,"b":[true,null,"x"]}`, 400)
	require.NoError(t, err)
	object, ok := value.(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, object["a"])
	require.Equal(t, []any{true, nil, "x"}, object["b"])
}

func TestParseBoundedJSONRejectsDuplicateKeys(t *testing.T) {
	_, err := ParseBoundedJSON(`{"a":1,"a":2}`, 400)
	require.Error(t, err)
}

func TestParseBoundedJSONRejectsTrailingGarbage(t *testing.T) {
	_, err := ParseBoundedJSON(`{"a":1} x`, 400)
	require.Error(t, err)
}

func TestNormalizeChatRequestBuildsPrompt(t *testing.T) {
	req, err := NormalizeChatRequest(map[string]any{
		"model": "gemini-argon",
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "hello"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, ModelArgon, req.Model)
	require.Equal(t, ThinkingExtended, req.ThinkingLevel)

	prompt, err := BuildCanonicalPrompt(req)
	require.NoError(t, err)
	require.Contains(t, prompt.Text, "[System Instructions]")
	require.Contains(t, prompt.Text, "be brief")
	require.Contains(t, prompt.Text, "[Conversation]\n[User]\nhello")
	require.Contains(t, prompt.Text, "Tool calls are prohibited for this turn")
}

func TestNormalizeChatRequestRejectsUnknownParameter(t *testing.T) {
	_, err := NormalizeChatRequest(map[string]any{
		"model":    "gemini-argon",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"top_k":    5.0,
	})
	require.Error(t, err)
	protocol, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, "UNSUPPORTED_PARAMETER", protocol.Code)
}

func TestNormalizeChatRequestThinkingSettings(t *testing.T) {
	_, err := NormalizeChatRequest(map[string]any{
		"model":            "gemini-argon",
		"reasoning_effort": "low",
		"thinking_level":   2.0,
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
	})
	require.Error(t, err)
	protocol, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, "CONFLICTING_THINKING_SETTINGS", protocol.Code)

	req, err := NormalizeChatRequest(map[string]any{
		"model":            "gemini-hydrogen",
		"reasoning_effort": "xhigh",
		"messages":         []any{map[string]any{"role": "user", "content": "hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, ThinkingExtended, req.ThinkingLevel)
	require.Equal(t, "xhigh", req.ReasoningEffort)
}

func TestNormalizeChatRequestToolHistoryPairing(t *testing.T) {
	_, err := NormalizeChatRequest(map[string]any{
		"model": "gemini-argon",
		"messages": []any{
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "get",
							"arguments": `{"x":1}`,
						},
					},
				},
			},
		},
	})
	require.Error(t, err)
	protocol, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, "PENDING_TOOL_CALLS", protocol.Code)
}

func TestBuildToolInstruction(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "look up weather",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": []any{"city"},
			},
		},
	}}
	instruction, policy, err := BuildToolInstruction(tools, "auto")
	require.NoError(t, err)
	require.True(t, policy.Enabled)
	require.Contains(t, instruction, "```tool_call")
	require.Contains(t, instruction, "get_weather")
}

func TestValidateToolArguments(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
			"days": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 7.0},
		},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
	require.NoError(t, ValidateToolArguments(map[string]any{"city": "Paris", "days": 3.0}, schema))
	require.Error(t, ValidateToolArguments(map[string]any{"days": 3.0}, schema))
	require.Error(t, ValidateToolArguments(map[string]any{"city": "Paris", "days": 9.0}, schema))
	require.Error(t, ValidateToolArguments(map[string]any{"city": "Paris", "extra": true}, schema))
}

func TestToolCallSplitterExtractsCall(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "get_weather",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		},
	}}
	policy, err := CompileToolPolicy(tools, "auto")
	require.NoError(t, err)
	splitter := NewToolCallSplitter(policy)

	events, err := splitter.Push("Let me check.\n```tool_call\n{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Paris\"}}\n```\n")
	require.NoError(t, err)
	_, err = splitter.Flush()
	require.NoError(t, err)

	var calls int
	var content string
	for _, event := range events {
		switch event.Type {
		case "call":
			calls++
			require.Equal(t, "get_weather", event.Call.Function.Name)
			require.JSONEq(t, `{"city":"Paris"}`, event.Call.Function.Arguments)
		case "content":
			content += event.Text
		}
	}
	require.Equal(t, 1, calls)
	require.Contains(t, content, "Let me check.")
}

func TestToolCallSplitterPassesOrdinaryFence(t *testing.T) {
	policy, err := CompileToolPolicy(nil, "auto")
	require.NoError(t, err)
	splitter := NewToolCallSplitter(policy)

	events, err := splitter.Push("```go\nfmt.Println(\"hi\")\n```\n")
	require.NoError(t, err)
	flushed, err := splitter.Flush()
	require.NoError(t, err)
	events = append(events, flushed...)

	var content string
	for _, event := range events {
		require.Equal(t, "content", event.Type)
		content += event.Text
	}
	require.Contains(t, content, "fmt.Println")
}

func TestToolCallSplitterRejectsUndeclaredTool(t *testing.T) {
	policy, err := CompileToolPolicy(nil, "auto")
	require.NoError(t, err)
	splitter := NewToolCallSplitter(policy)
	_, err = splitter.Push("```tool_call\n{\"name\":\"nope\",\"arguments\":{}}\n```\n")
	require.Error(t, err)
}

func chatBase() map[string]any {
	return map[string]any{
		"model":    ModelArgon,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
}

func chatWith(extra map[string]any) map[string]any {
	payload := chatBase()
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func TestNormalizeResponsesRequestInstructionsAndStringInput(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"model":        ModelArgon,
		"instructions": "be brief",
		"input":        "hello",
	})
	require.NoError(t, err)
	require.Equal(t, ModelArgon, req.Model)
	require.Len(t, req.Messages, 2)
	require.Equal(t, "system", req.Messages[0].Role)
	require.Equal(t, "be brief", req.Messages[0].Content)
	require.Equal(t, "user", req.Messages[1].Role)
	require.Equal(t, "hello", req.Messages[1].Content)

	prompt, err := BuildCanonicalPrompt(req)
	require.NoError(t, err)
	require.Contains(t, prompt.Text, "be brief")
	require.Contains(t, prompt.Text, "[Conversation]\n[User]\nhello")
}

func TestNormalizeResponsesRequestArrayInput(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"type": "message", "role": "assistant", "content": "second"},
			map[string]any{"type": "input_text", "text": "third"},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 3)
	require.Equal(t, "user", req.Messages[0].Role)
	require.Equal(t, "first", req.Messages[0].Content)
	require.Equal(t, "assistant", req.Messages[1].Role)
	require.Equal(t, "second", req.Messages[1].Content)
	require.Equal(t, "user", req.Messages[2].Role)
	require.Equal(t, "third", req.Messages[2].Content)
}

func TestNormalizeResponsesRequestFunctionCallPairing(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "assistant", "content": "let me look"},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"Paris"}`},
			map[string]any{"type": "function_call", "call_id": "call_2", "name": "get_time", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
			map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "noon"},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 3)
	require.Equal(t, "assistant", req.Messages[0].Role)
	require.Len(t, req.Messages[0].ToolCalls, 2)
	require.Equal(t, "call_1", req.Messages[0].ToolCalls[0].ID)
	require.JSONEq(t, `{"city":"Paris"}`, req.Messages[0].ToolCalls[0].Function.Arguments)
	require.Equal(t, "tool", req.Messages[1].Role)
	require.Equal(t, "call_1", req.Messages[1].ToolCallID)
	require.Equal(t, "sunny", req.Messages[1].Content)
	require.Equal(t, "tool", req.Messages[2].Role)
	require.Equal(t, "call_2", req.Messages[2].ToolCallID)

	_, err = BuildCanonicalPrompt(req)
	require.NoError(t, err)
}

func TestNormalizeResponsesRequestFunctionCallWithoutPreviousAssistant(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "get", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 2)
	require.Equal(t, "assistant", req.Messages[0].Role)
	require.Empty(t, req.Messages[0].Content)
	require.Len(t, req.Messages[0].ToolCalls, 1)
	require.Equal(t, "tool", req.Messages[1].Role)
}

func TestNormalizeResponsesRequestToolConversion(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"input": "hi",
		"tools": []any{map[string]any{
			"type":        "function",
			"name":        "get_weather",
			"description": "look up weather",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		}},
		"tool_choice": map[string]any{"type": "function", "name": "get_weather"},
	})
	require.NoError(t, err)
	require.Len(t, req.Tools, 1)
	tool, ok := req.Tools[0].(map[string]any)
	require.True(t, ok)
	fn, ok := tool["function"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "get_weather", fn["name"])
	require.Equal(t, map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}, req.ToolChoice)
	require.NotNil(t, req.Policy)
	require.Equal(t, "get_weather", req.Policy.Forced)

	instruction, _, err := BuildToolInstruction(req.Tools, req.ToolChoice)
	require.NoError(t, err)
	require.Contains(t, instruction, "get_weather")
}

func TestNormalizeResponsesRequestRejectsImages(t *testing.T) {
	_, err := NormalizeResponsesRequest(map[string]any{
		"input": []any{map[string]any{"type": "input_image", "image_url": "http://example.com/x.png"}},
	})
	requireProtocolError(t, err, "UNSUPPORTED_IMAGE_INPUT", 400)

	_, err = NormalizeResponsesRequest(map[string]any{
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_image", "image_url": "x"}},
		}},
	})
	requireProtocolError(t, err, "UNSUPPORTED_IMAGE_INPUT", 400)
}

func TestNormalizeResponsesRequestErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		code    string
		status  int
	}{
		{"unsupported model", map[string]any{"model": "gpt-4", "input": "hi"}, "UNSUPPORTED_MODEL", 400},
		{"unsupported parameter", map[string]any{"input": "hi", "top_k": 1.0}, "UNSUPPORTED_PARAMETER", 400},
		{"include not list", map[string]any{"input": "hi", "include": "reasoning.encrypted_content"}, "UNSUPPORTED_INCLUDE", 400},
		{"include unsupported value", map[string]any{"input": "hi", "include": []any{"foo"}}, "UNSUPPORTED_INCLUDE", 400},
		{"include non string", map[string]any{"input": "hi", "include": []any{1.0}}, "UNSUPPORTED_INCLUDE", 400},
		{"invalid instructions", map[string]any{"input": "hi", "instructions": 1.0}, "INVALID_REQUEST", 400},
		{"missing input", map[string]any{"model": ModelArgon}, "INPUT_REQUIRED", 400},
		{"null input", map[string]any{"input": nil}, "INPUT_REQUIRED", 400},
		{"empty input array", map[string]any{"input": []any{}}, "INPUT_REQUIRED", 400},
		{"unsupported input type", map[string]any{"input": []any{map[string]any{"type": "audio"}}}, "UNSUPPORTED_INPUT_TYPE", 400},
		{"invalid input item", map[string]any{"input": []any{"text"}}, "INVALID_INPUT_ITEM", 400},
		{"unknown message item key", map[string]any{"input": []any{map[string]any{"type": "message", "role": "user", "content": "hi", "extra": 1.0}}}, "INVALID_INPUT_ITEM", 400},
		{"unsupported message role", map[string]any{"input": []any{map[string]any{"type": "message", "role": "robot", "content": "hi"}}}, "UNSUPPORTED_ROLE", 400},
		{"function call bad id", map[string]any{"input": []any{map[string]any{"type": "function_call", "call_id": "bad id", "name": "f", "arguments": `{}`}}}, "INVALID_INPUT_ITEM", 400},
		{"function call empty name", map[string]any{"input": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "", "arguments": `{}`}}}, "INVALID_INPUT_ITEM", 400},
		{"function call namespace", map[string]any{"input": []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "f", "namespace": "ns", "arguments": `{}`}}}, "UNSUPPORTED_TOOL_TYPE", 400},
		{"function output unknown key", map[string]any{"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "x", "extra": 1.0}}}, "INVALID_INPUT_ITEM", 400},
		{"input text unknown key", map[string]any{"input": []any{map[string]any{"type": "input_text", "text": "x", "extra": 1.0}}}, "INVALID_INPUT_ITEM", 400},
		{"undefined tool type", map[string]any{"input": "hi", "tools": []any{map[string]any{"type": "web_search"}}}, "UNSUPPORTED_TOOL_TYPE", 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeResponsesRequest(test.payload)
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestNormalizeResponsesRequestNativeSearch(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{"input": "hi", "native_search": true})
	require.NoError(t, err)
	require.True(t, req.NativeSearch)

	tools := []any{map[string]any{
		"type":       "function",
		"name":       "get_weather",
		"parameters": map[string]any{"type": "object"},
	}}
	tests := []struct {
		name    string
		payload map[string]any
		code    string
		status  int
	}{
		{"wrong model", map[string]any{"input": "hi", "model": ModelHydrogen, "native_search": true}, "UNSUPPORTED_NATIVE_SEARCH_MODE", 400},
		{"wrong thinking", map[string]any{"input": "hi", "reasoning_effort": "low", "native_search": true}, "UNSUPPORTED_NATIVE_SEARCH_MODE", 400},
		{"tool conflict", map[string]any{"input": "hi", "native_search": true, "tools": tools}, "NATIVE_SEARCH_TOOL_CONFLICT", 400},
		{"parallel conflict", map[string]any{"input": "hi", "native_search": true, "parallel_tool_calls": true}, "NATIVE_SEARCH_TOOL_CONFLICT", 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeResponsesRequest(test.payload)
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestNormalizeResponsesRequestAcceptsResponsesOnlyKeys(t *testing.T) {
	req, err := NormalizeResponsesRequest(map[string]any{
		"input":             "hi",
		"include":           []any{"reasoning.encrypted_content"},
		"max_output_tokens": 100.0,
		"text":              map[string]any{"format": map[string]any{"type": "text"}},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "user", req.Messages[0].Role)
}

func TestNormalizeResponsesToolsValidation(t *testing.T) {
	_, _, err := normalizeResponsesTools("not-a-list", nil)
	requireProtocolError(t, err, "INVALID_TOOLS", 400)

	_, _, err = normalizeResponsesTools([]any{"tool"}, nil)
	requireProtocolError(t, err, "INVALID_TOOL_DECLARATION", 400)

	_, _, err = normalizeResponsesTools([]any{map[string]any{"type": "web_search"}}, nil)
	requireProtocolError(t, err, "UNSUPPORTED_TOOL_TYPE", 400)

	tools, choice, err := normalizeResponsesTools([]any{map[string]any{"type": "function", "name": "f", "description": "d"}}, "none")
	require.NoError(t, err)
	require.Equal(t, "none", choice)
	require.Len(t, tools, 1)
	converted, ok := tools[0].(map[string]any)
	require.True(t, ok)
	_, nested := converted["function"]
	require.True(t, nested)

	_, _, err = normalizeResponsesTools(nil, map[string]any{"type": "web_search"})
	requireProtocolError(t, err, "UNSUPPORTED_TOOL_CHOICE", 400)

	_, _, err = normalizeResponsesTools(nil, map[string]any{"type": "function", "name": ""})
	requireProtocolError(t, err, "UNSUPPORTED_TOOL_CHOICE", 400)

	_, _, err = normalizeResponsesTools(nil, 1.0)
	requireProtocolError(t, err, "UNSUPPORTED_TOOL_CHOICE", 400)

	_, choice, err = normalizeResponsesTools(nil, map[string]any{"type": "function", "function": map[string]any{"name": "f"}})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"type": "function", "function": map[string]any{"name": "f"}}, choice)
}

func TestNormalizeChatRequestAcceptedBranches(t *testing.T) {
	req, err := NormalizeChatRequest(chatWith(map[string]any{
		"model":                 "models/gemini-hydrogen",
		"stream":                true,
		"stream_options":        map[string]any{"include_usage": true},
		"store":                 false,
		"parallel_tool_calls":   false,
		"max_tokens":            100.0,
		"max_completion_tokens": 200.0,
		"temperature":           0.5,
	}))
	require.NoError(t, err)
	require.Equal(t, ModelHydrogen, req.Model)
	require.True(t, req.Stream)
	require.True(t, req.IncludeUsage)
	require.NotNil(t, req.ParallelToolCalls)
	require.False(t, *req.ParallelToolCalls)
	require.Equal(t, []string{"max_tokens", "max_completion_tokens"}, req.IgnoredTokenParameters)

	defaulted, err := NormalizeChatRequest(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, ModelArgon, defaulted.Model)
	require.Equal(t, ThinkingExtended, defaulted.ThinkingLevel)

	reasoned, err := NormalizeChatRequest(chatWith(map[string]any{
		"reasoning": map[string]any{"summary": "none", "effort": "high"},
	}))
	require.NoError(t, err)
	require.Equal(t, ThinkingExtended, reasoned.ThinkingLevel)
	require.Equal(t, "high", reasoned.ReasoningEffort)

	explicit, err := NormalizeChatRequest(chatWith(map[string]any{"thinking_level": 2.0}))
	require.NoError(t, err)
	require.Equal(t, ThinkingExtended, explicit.ThinkingLevel)
}

func TestNormalizeChatRequestErrorBranches(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		code    string
		status  int
	}{
		{"unsupported model", chatWith(map[string]any{"model": "gemini-pro"}), "UNSUPPORTED_MODEL", 400},
		{"non string model", chatWith(map[string]any{"model": 1.0}), "UNSUPPORTED_MODEL", 400},
		{"unknown model suffix", chatWith(map[string]any{"model": "gemini-argon-x"}), "UNSUPPORTED_MODEL", 400},
		{"unsupported parameter", chatWith(map[string]any{"top_k": 1.0}), "UNSUPPORTED_PARAMETER", 400},
		{"stream not bool", chatWith(map[string]any{"stream": "yes"}), "INVALID_STREAM", 400},
		{"stream options not object", chatWith(map[string]any{"stream_options": []any{}}), "INVALID_STREAM_OPTIONS", 400},
		{"stream options unknown key", chatWith(map[string]any{"stream": true, "stream_options": map[string]any{"foo": true}}), "INVALID_STREAM_OPTIONS", 400},
		{"include usage without stream", chatWith(map[string]any{"stream_options": map[string]any{"include_usage": true}}), "INVALID_STREAM_OPTIONS", 400},
		{"include usage not bool", chatWith(map[string]any{"stream": true, "stream_options": map[string]any{"include_usage": "yes"}}), "INVALID_STREAM_OPTIONS", 400},
		{"store true", chatWith(map[string]any{"store": true}), "UNSUPPORTED_STORE", 400},
		{"store not bool", chatWith(map[string]any{"store": "yes"}), "UNSUPPORTED_STORE", 400},
		{"parallel tool calls not bool", chatWith(map[string]any{"parallel_tool_calls": "yes"}), "INVALID_PARALLEL_TOOL_CALLS", 400},
		{"native search not bool", chatWith(map[string]any{"native_search": "yes"}), "INVALID_NATIVE_SEARCH", 400},
		{"invalid reasoning effort", chatWith(map[string]any{"reasoning_effort": "ultra"}), "INVALID_REASONING_EFFORT", 400},
		{"non string reasoning effort", chatWith(map[string]any{"reasoning_effort": 1.0}), "INVALID_REASONING_EFFORT", 400},
		{"reasoning not object", chatWith(map[string]any{"reasoning": "high"}), "UNSUPPORTED_REASONING", 400},
		{"reasoning unknown key", chatWith(map[string]any{"reasoning": map[string]any{"foo": "bar"}}), "UNSUPPORTED_REASONING", 400},
		{"reasoning summary not none", chatWith(map[string]any{"reasoning": map[string]any{"summary": "auto"}}), "UNSUPPORTED_REASONING", 400},
		{"reasoning summary not string", chatWith(map[string]any{"reasoning": map[string]any{"summary": 1.0}}), "UNSUPPORTED_REASONING", 400},
		{"reasoning effort invalid", chatWith(map[string]any{"reasoning": map[string]any{"effort": "ultra"}}), "INVALID_REASONING_EFFORT", 400},
		{"reasoning effort not string", chatWith(map[string]any{"reasoning": map[string]any{"effort": 1.0}}), "INVALID_REASONING_EFFORT", 400},
		{"conflicting reasoning", chatWith(map[string]any{"reasoning": map[string]any{"effort": "low"}, "reasoning_effort": "medium"}), "CONFLICTING_THINKING_SETTINGS", 400},
		{"deep think", chatWith(map[string]any{"thinking_level": 3.0}), "NATIVE_DEEP_THINK_UNSUPPORTED", 422},
		{"thinking level out of range", chatWith(map[string]any{"thinking_level": 5.0}), "INVALID_THINKING_LEVEL", 400},
		{"thinking level negative", chatWith(map[string]any{"thinking_level": -1.0}), "INVALID_THINKING_LEVEL", 400},
		{"thinking level fraction", chatWith(map[string]any{"thinking_level": 1.5}), "INVALID_THINKING_LEVEL", 400},
		{"thinking level not number", chatWith(map[string]any{"thinking_level": "2"}), "INVALID_THINKING_LEVEL", 400},
		{"messages not list", chatWith(map[string]any{"messages": "hi"}), "MESSAGES_REQUIRED", 400},
		{"messages empty", chatWith(map[string]any{"messages": []any{}}), "MESSAGES_REQUIRED", 400},
		{"missing messages", map[string]any{"model": ModelArgon}, "MESSAGES_REQUIRED", 400},
		{"message not object", chatWith(map[string]any{"messages": []any{"hi"}}), "INVALID_MESSAGE", 400},
		{"unsupported role", chatWith(map[string]any{"messages": []any{map[string]any{"role": "robot", "content": "hi"}}}), "UNSUPPORTED_ROLE", 400},
		{"message unknown key", chatWith(map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi", "name": "x"}}}), "UNSUPPORTED_PARAMETER", 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeChatRequest(test.payload)
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestNormalizeChatRequestRejectsTooManyMessages(t *testing.T) {
	messages := make([]any, maxMessages+1)
	for i := range messages {
		messages[i] = map[string]any{"role": "user", "content": "hi"}
	}
	_, err := NormalizeChatRequest(map[string]any{"model": ModelArgon, "messages": messages})
	requireProtocolError(t, err, "MESSAGES_REQUIRED", 400)
}

func TestNormalizeChatRequestMessageContent(t *testing.T) {
	req, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": nil},
		map[string]any{"role": "user", "content": "hi"},
	}}))
	require.NoError(t, err)
	require.Empty(t, req.Messages[0].Content)

	joined, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "input_text", "text": "b"},
			map[string]any{"type": "output_text", "text": "c"},
		},
	}}}))
	require.NoError(t, err)
	require.Equal(t, "abc", joined.Messages[0].Content)

	tests := []struct {
		name     string
		messages []any
		code     string
	}{
		{"nil user content", []any{map[string]any{"role": "user", "content": nil}}, "INVALID_MESSAGE_CONTENT"},
		{"empty content array", []any{map[string]any{"role": "user", "content": []any{}}}, "INVALID_MESSAGE_CONTENT"},
		{"non object part", []any{map[string]any{"role": "user", "content": []any{"x"}}}, "INVALID_MESSAGE_CONTENT"},
		{"image url part", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}}}}}, "UNSUPPORTED_IMAGE_INPUT"},
		{"input image part", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "x"}}}}, "UNSUPPORTED_IMAGE_INPUT"},
		{"unknown part type", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "audio", "text": "x"}}}}, "UNSUPPORTED_CONTENT_TYPE"},
		{"non string text", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": 1.0}}}}, "INVALID_MESSAGE_CONTENT"},
		{"user tool calls", []any{map[string]any{"role": "user", "content": "hi", "tool_calls": []any{}}}, "INVALID_TOOL_CALL"},
		{"user tool call id", []any{map[string]any{"role": "user", "content": "hi", "tool_call_id": "call_1"}}, "INVALID_TOOL_RESULT"},
		{"assistant refusal", []any{map[string]any{"role": "assistant", "content": "x", "refusal": "no"}}, "UNSUPPORTED_PARAMETER"},
		{"assistant parsed", []any{map[string]any{"role": "assistant", "content": "x", "parsed": map[string]any{}}}, "UNSUPPORTED_PARAMETER"},
		{"user refusal nil", []any{map[string]any{"role": "user", "content": "hi", "refusal": nil}}, "UNSUPPORTED_PARAMETER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeChatRequest(chatWith(map[string]any{"messages": test.messages}))
			requireProtocolError(t, err, test.code, 400)
		})
	}

	allowed, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": "x", "refusal": nil},
		map[string]any{"role": "user", "content": "hi"},
	}}))
	require.NoError(t, err)
	require.Len(t, allowed.Messages, 2)
}

func TestNormalizeChatRequestToolCallValidation(t *testing.T) {
	assistant := func(calls any) map[string]any {
		return map[string]any{"messages": []any{
			map[string]any{"role": "assistant", "content": nil, "tool_calls": calls},
		}}
	}
	call := func(id, callType, args string) map[string]any {
		return map[string]any{
			"id":       id,
			"type":     callType,
			"function": map[string]any{"name": "get", "arguments": args},
		}
	}

	tests := []struct {
		name string
		call any
		code string
	}{
		{"empty call list", []any{}, "INVALID_TOOL_CALL"},
		{"not object", []any{"call"}, "INVALID_TOOL_CALL"},
		{"unknown key", []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "get", "arguments": `{}`}, "extra": 1.0}}, "INVALID_TOOL_CALL"},
		{"invalid id", []any{call("bad id", "function", `{}`)}, "INVALID_TOOL_CALL"},
		{"duplicate id", []any{call("call_1", "function", `{}`), call("call_1", "function", `{}`)}, "INVALID_TOOL_CALL"},
		{"wrong type", []any{call("call_1", "other", `{}`)}, "INVALID_TOOL_CALL"},
		{"missing function", []any{map[string]any{"id": "call_1", "type": "function"}}, "INVALID_TOOL_CALL"},
		{"function unknown key", []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "get", "arguments": `{}`, "extra": 1.0}}}, "INVALID_TOOL_CALL"},
		{"invalid json", []any{call("call_1", "function", `{`)}, "INVALID_TOOL_JSON"},
		{"non object arguments", []any{call("call_1", "function", `[1,2]`)}, "INVALID_TOOL_ARGUMENTS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeChatRequest(chatWith(assistant(test.call)))
			requireProtocolError(t, err, test.code, 400)
		})
	}

	canonical, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			call("call_1", "function", `{"b":2,"a":1}`),
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "ok"},
	}}))
	require.NoError(t, err)
	require.JSONEq(t, `{"a":1,"b":2}`, canonical.Messages[0].ToolCalls[0].Function.Arguments)

	pending, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			call("call_1", "function", `{}`),
		}},
		map[string]any{"role": "user", "content": "hi"},
	}}))
	require.Nil(t, pending)
	requireProtocolError(t, err, "PENDING_TOOL_CALLS", 400)

	unknownResult, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "tool", "tool_call_id": "call_9", "content": "ok"},
	}}))
	require.Nil(t, unknownResult)
	requireProtocolError(t, err, "INVALID_TOOL_RESULT", 400)

	invalidResultID, err := NormalizeChatRequest(chatWith(map[string]any{"messages": []any{
		map[string]any{"role": "tool", "tool_call_id": "bad id", "content": "ok"},
	}}))
	require.Nil(t, invalidResultID)
	requireProtocolError(t, err, "INVALID_TOOL_RESULT", 400)
}

func TestNormalizeChatRequestToolPolicyEdges(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "get_weather",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
			},
		},
	}}

	required, err := NormalizeChatRequest(chatWith(map[string]any{"tools": tools, "tool_choice": "required"}))
	require.NoError(t, err)
	require.True(t, required.Policy.Enabled)
	require.Equal(t, "required", required.Policy.Choice)

	forced, err := NormalizeChatRequest(chatWith(map[string]any{
		"tools":       tools,
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
	}))
	require.NoError(t, err)
	require.Equal(t, "get_weather", forced.Policy.Forced)
	require.Empty(t, forced.Policy.Choice)

	disabled, err := NormalizeChatRequest(chatWith(map[string]any{"tools": tools, "tool_choice": "none"}))
	require.NoError(t, err)
	require.False(t, disabled.Policy.Enabled)

	tests := []struct {
		name   string
		extra  map[string]any
		code   string
		status int
	}{
		{"required without tools", map[string]any{"tool_choice": "required"}, "TOOLS_REQUIRED", 400},
		{"forced undeclared", map[string]any{"tools": tools, "tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "nope"}}}, "UNDECLARED_TOOL", 400},
		{"invalid string choice", map[string]any{"tool_choice": "maybe"}, "UNSUPPORTED_TOOL_CHOICE", 400},
		{"non string choice", map[string]any{"tool_choice": true}, "UNSUPPORTED_TOOL_CHOICE", 400},
		{"tools not list", map[string]any{"tools": "nope"}, "INVALID_TOOLS", 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeChatRequest(chatWith(test.extra))
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestNormalizeChatRequestStripsSchemaAnnotations(t *testing.T) {
	req, err := NormalizeChatRequest(chatWith(map[string]any{"tools": []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup",
			"description": "d",
			"parameters": map[string]any{
				"type":     "object",
				"$schema":  "http://json-schema.org/draft-07/schema#",
				"title":    "Lookup",
				"example":  "not-an-annotation-key-but-untouched",
				"examples": []any{},
				"default":  map[string]any{},
				"properties": map[string]any{
					"day": map[string]any{"type": "string", "default": "x", "format": "date"},
				},
			},
		},
	}}}))
	require.NoError(t, err)
	require.Len(t, req.Tools, 1)
	tool := req.Tools[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	parameters := fn["parameters"].(map[string]any)
	require.NotContains(t, parameters, "$schema")
	require.NotContains(t, parameters, "title")
	require.NotContains(t, parameters, "examples")
	require.NotContains(t, parameters, "default")
	require.Contains(t, parameters, "example")
	day := parameters["properties"].(map[string]any)["day"].(map[string]any)
	require.NotContains(t, day, "default")
	require.NotContains(t, day, "format")
	require.Equal(t, "string", day["type"])
}

func TestNormalizeResponsesFunctionCallOutputAcceptsContentParts(t *testing.T) {
	payload := map[string]any{
		"model": "gemini-argon",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "run it"},
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": `{"q":"x"}`,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output": []any{
					map[string]any{"type": "input_text", "text": "part one "},
					map[string]any{"type": "output_text", "text": "part two"},
				},
			},
		},
	}

	req, err := NormalizeResponsesRequest(payload)
	require.NoError(t, err)
	require.Len(t, req.Messages, 3)
	require.Equal(t, "tool", req.Messages[2].Role)
	require.Equal(t, "part one part two", req.Messages[2].Content)
}

func TestNormalizeResponsesRejectsNonTextValues(t *testing.T) {
	cases := map[string]any{
		"non-string output": map[string]any{"type": "function_call_output", "call_id": "call_1", "output": 42},
		"image output part": map[string]any{
			"type": "function_call_output", "call_id": "call_1",
			"output": []any{map[string]any{"type": "input_image", "image_url": "https://x/y.png"}},
		},
		"non-string text": map[string]any{"type": "input_text", "text": 7},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			payload := map[string]any{
				"model": "gemini-argon",
				"input": []any{
					map[string]any{"type": "message", "role": "user", "content": "hi"},
					item,
				},
			}
			_, err := NormalizeResponsesRequest(payload)
			require.Error(t, err)
			protocol, ok := AsError(err)
			require.True(t, ok)
			require.Equal(t, "INVALID_INPUT_ITEM", protocol.Code)
		})
	}
}

// Responses 请求显式给了 tool_choice 却未声明任何工具时，不能静默降级成 auto：
// 必须和 Chat 路径一样报 TOOLS_REQUIRED / UNDECLARED_TOOL。
func TestNormalizeResponsesRequestToolChoiceWithoutTools(t *testing.T) {
	t.Run("required without tools is rejected", func(t *testing.T) {
		_, err := NormalizeResponsesRequest(map[string]any{
			"model":       ModelArgon,
			"input":       "hi",
			"tool_choice": "required",
		})
		requireProtocolError(t, err, "TOOLS_REQUIRED", 400)
	})

	t.Run("forced undeclared function is rejected", func(t *testing.T) {
		_, err := NormalizeResponsesRequest(map[string]any{
			"model": ModelArgon,
			"input": "hi",
			"tool_choice": map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "lookup"},
			},
		})
		requireProtocolError(t, err, "UNDECLARED_TOOL", 400)
	})

	t.Run("auto without tools is accepted", func(t *testing.T) {
		req, err := NormalizeResponsesRequest(map[string]any{
			"model":       ModelArgon,
			"input":       "hi",
			"tool_choice": "auto",
		})
		require.NoError(t, err)
		require.Empty(t, req.Tools)
	})

	t.Run("none without tools is accepted", func(t *testing.T) {
		req, err := NormalizeResponsesRequest(map[string]any{
			"model":       ModelArgon,
			"input":       "hi",
			"tool_choice": "none",
		})
		require.NoError(t, err)
		require.Empty(t, req.Tools)
	})

	t.Run("absent tool_choice without tools is accepted", func(t *testing.T) {
		req, err := NormalizeResponsesRequest(map[string]any{
			"model": ModelArgon,
			"input": "hi",
		})
		require.NoError(t, err)
		require.Empty(t, req.Tools)
	})
}

// thinking_level 的合法区间是 0..4（4 为最高同步档）。3 是异步 Deep Think，
// 解析通过但转发前必须明确拒绝，而不是静默降级。
func TestNormalizeChatRequestThinkingLevelBounds(t *testing.T) {
	chatWithLevel := func(level any) map[string]any {
		return map[string]any{
			"model":          ModelArgon,
			"thinking_level": level,
			"messages":       []any{map[string]any{"role": "user", "content": "hi"}},
		}
	}

	req, err := NormalizeChatRequest(chatWithLevel(4.0))
	require.NoError(t, err)
	require.Equal(t, ThinkingMax, req.ThinkingLevel)

	for _, level := range []any{-1.0, 5.0, 2.5, "2"} {
		_, err := NormalizeChatRequest(chatWithLevel(level))
		requireProtocolError(t, err, "INVALID_THINKING_LEVEL", 400)
	}

	_, err = NormalizeChatRequest(chatWithLevel(3.0))
	requireProtocolError(t, err, "NATIVE_DEEP_THINK_UNSUPPORTED", 422)
}
