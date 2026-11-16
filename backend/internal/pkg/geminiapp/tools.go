package geminiapp

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Caller-owned prompt tool protocol: declarations are embedded in the prompt and
// tool calls are requested through a fenced `tool_call` block. The provider never
// executes tools. Mirrors prompt-tool-schema.mjs and buildToolInstruction().

const (
	maxTools       = 64
	maxToolCalls   = 32
	maxToolBytes   = 256 * 1024
	maxSchemaDepth = 16
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ToolPolicy is the compiled registry of declared tools.
type ToolPolicy struct {
	Registry map[string]map[string]any
	// Choice is "auto", "none" or "required"; empty when Forced is set.
	Choice  string
	Forced  string
	Enabled bool
}

// CompileToolPolicy validates declarations and tool_choice.
func CompileToolPolicy(tools []any, choice any) (*ToolPolicy, error) {
	registry := make(map[string]map[string]any, len(tools))
	if len(tools) > maxTools {
		return nil, NewError("TOOL_SIZE_LIMIT", 400)
	}
	totalBytes := 0
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return nil, NewError("INVALID_TOOL_DECLARATION", 400)
		}
		if err := onlyKeys(tool, "type", "function"); err != nil {
			return nil, err
		}
		if toolType, _ := tool["type"].(string); toolType != "function" {
			return nil, NewError("UNSUPPORTED_TOOL_TYPE", 400)
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			return nil, NewError("INVALID_TOOL_DECLARATION", 400)
		}
		if err := onlyKeys(fn, "name", "description", "parameters", "strict"); err != nil {
			return nil, err
		}
		name, _ := fn["name"].(string)
		if !toolNamePattern.MatchString(name) {
			return nil, NewError("INVALID_TOOL_DECLARATION", 400)
		}
		if _, duplicate := registry[name]; duplicate {
			return nil, NewError("INVALID_TOOL_DECLARATION", 400)
		}
		if description, exists := fn["description"]; exists {
			if _, ok := description.(string); !ok {
				return nil, NewError("INVALID_TOOL_DECLARATION", 400)
			}
		}
		if strict, exists := fn["strict"]; exists {
			if _, ok := strict.(bool); !ok {
				return nil, NewError("INVALID_TOOL_DECLARATION", 400)
			}
		}
		parameters := map[string]any{"type": "object"}
		if declared, exists := fn["parameters"]; exists {
			schema, ok := declared.(map[string]any)
			if !ok || schema["type"] != "object" {
				return nil, NewError("UNSUPPORTED_TOOL_SCHEMA", 400)
			}
			parameters = schema
		}
		if err := validateSchemaShape(parameters, 0); err != nil {
			return nil, err
		}
		encoded, err := marshalJSON(tool)
		if err != nil {
			return nil, NewError("INVALID_TOOL_DECLARATION", 400)
		}
		totalBytes += len(encoded)
		if totalBytes > maxToolBytes {
			return nil, NewError("TOOL_SIZE_LIMIT", 400)
		}
		registry[name] = parameters
	}

	policy := &ToolPolicy{Registry: registry, Choice: "auto"}
	switch value := choice.(type) {
	case nil:
	case string:
		if value != "auto" && value != "none" && value != "required" {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		policy.Choice = value
	case map[string]any:
		if err := onlyKeys(value, "type", "function"); err != nil {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		if value["type"] != "function" {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		fn, ok := value["function"].(map[string]any)
		if !ok {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		if err := onlyKeys(fn, "name"); err != nil {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		name, _ := fn["name"].(string)
		if name == "" {
			return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
		}
		if _, ok := registry[name]; !ok {
			return nil, NewError("UNDECLARED_TOOL", 400)
		}
		policy.Forced = name
		policy.Choice = ""
	default:
		return nil, NewError("UNSUPPORTED_TOOL_CHOICE", 400)
	}
	if (policy.Choice == "required" || policy.Forced != "") && len(registry) == 0 {
		return nil, NewError("TOOLS_REQUIRED", 400)
	}
	policy.Enabled = policy.Choice != "none" && len(registry) > 0
	return policy, nil
}

func onlyKeys(object map[string]any, allowed ...string) error {
	for key := range object {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return NewErrorf("UNSUPPORTED_TOOL_SCHEMA", 400, "unexpected key %s", key)
		}
	}
	return nil
}

// BuildToolInstruction renders the prompt preamble for caller-owned tools.
func BuildToolInstruction(tools []any, choice any) (string, *ToolPolicy, error) {
	policy, err := CompileToolPolicy(tools, choice)
	if err != nil {
		return "", nil, err
	}
	if !policy.Enabled {
		return "Answer the latest user request now as the assistant. If caller tool results are present, use them to complete your answer; do not wait for another user message. Never invent tool outputs. Tool calls are prohibited for this turn. Do not emit a tool_call fence. For literal examples use a json fence.", policy, nil
	}
	encoded, err := marshalJSON(tools)
	if err != nil {
		return "", nil, NewError("INVALID_TOOL_DECLARATION", 400)
	}
	lines := []string{
		"# Available Local Tools (caller-owned prompt protocol, NOT native function calling)",
		encoded,
		"To REQUEST execution, emit a top-level fenced block on separate lines:",
		"```tool_call",
		`{"name":"declared_name","arguments":{}}`,
		"```",
		"Arguments must be a JSON object matching the declared schema. Never invent tools or results.",
		"Only the caller executes tools. Wait for linked caller tool results before using results.",
		"A standalone top-level tool_call fence is reserved for actual requests; use json fences for literal examples. Inline references are text.",
	}
	switch {
	case policy.Forced != "":
		lines = append(lines, "You MUST request only "+policy.Forced+".")
	case policy.Choice == "required":
		lines = append(lines, "You MUST request at least one declared tool.")
	default:
		lines = append(lines, "Request tools only when needed.")
	}
	return strings.Join(lines, "\n"), policy, nil
}

// ValidateToolCall checks a decoded call against the policy.
func ValidateToolCall(name string, args map[string]any, policy *ToolPolicy) error {
	if policy == nil || !policy.Enabled {
		return NewError("TOOL_CALL_NOT_ALLOWED", 502)
	}
	schema, ok := policy.Registry[name]
	if !ok {
		return NewError("UNDECLARED_TOOL", 502)
	}
	if policy.Forced != "" && name != policy.Forced {
		return NewError("TOOL_CHOICE_MISMATCH", 502)
	}
	if err := ValidateToolArguments(args, schema); err != nil {
		if protocol, ok := AsError(err); ok && protocol.Code == "INVALID_TOOL_ARGUMENTS" {
			return NewError("INVALID_TOOL_ARGUMENTS", 502)
		}
		return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 502)
	}
	return nil
}

// ValidateToolCompletion enforces tool_choice == required / forced.
func ValidateToolCompletion(callCount int, policy *ToolPolicy) error {
	if policy != nil && (policy.Choice == "required" || policy.Forced != "") && callCount == 0 {
		return NewError("TOOL_CHOICE_NOT_SATISFIED", 502)
	}
	return nil
}

// ValidateToolArguments validates a parsed arguments object against a schema.
func ValidateToolArguments(value any, schema map[string]any) error {
	return validateValue(value, schema, 0)
}

func validateValue(value any, schema map[string]any, depth int) error {
	if depth > maxSchemaDepth {
		return NewError("TOOL_SIZE_LIMIT", 400)
	}
	nullable, _ := schema["nullable"].(bool)
	if value == nil {
		if nullable || schemaTypeIs(schema, "null") {
			return nil
		}
		return NewError("INVALID_TOOL_ARGUMENTS", 400)
	}
	if enum, ok := schema["enum"].([]any); ok && !containsLiteral(enum, value) {
		return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
	}
	if constant, exists := schema["const"]; exists && !literalEqual(constant, value) {
		return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
	}
	if expected := schemaTypes(schema); len(expected) > 0 && !matchesAnyType(value, expected) {
		return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
	}
	if number, ok := value.(float64); ok {
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
		}
	}
	if object, ok := value.(map[string]any); ok {
		if required, ok := schema["required"].([]any); ok {
			for _, name := range required {
				key, _ := name.(string)
				if _, exists := object[key]; !exists {
					return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		additional, hasAdditional := schema["additionalProperties"]
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, declared := properties[key].(map[string]any)
			if declared {
				if err := validateValue(object[key], child, depth+1); err != nil {
					return err
				}
				continue
			}
			if hasAdditional {
				switch extra := additional.(type) {
				case bool:
					if !extra {
						return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
					}
				case map[string]any:
					if err := validateValue(object[key], extra, depth+1); err != nil {
						return err
					}
				}
			}
		}
	}
	if array, ok := value.([]any); ok {
		if items, ok := schema["items"].(map[string]any); ok {
			for _, item := range array {
				if err := validateValue(item, items, depth+1); err != nil {
					return err
				}
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		matches := 0
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if validateValue(value, branch, depth+1) == nil {
				matches++
			}
		}
		if keyword == "anyOf" && matches == 0 {
			return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
		}
		if keyword == "oneOf" && matches != 1 {
			return NewError("TOOL_ARGUMENT_SCHEMA_MISMATCH", 400)
		}
	}
	if branches, ok := schema["allOf"].([]any); ok {
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if err := validateValue(value, branch, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaTypeIs(schema map[string]any, want string) bool {
	for _, candidate := range schemaTypes(schema) {
		if candidate == want {
			return true
		}
	}
	return false
}

func schemaTypes(schema map[string]any) []string {
	switch value := schema["type"].(type) {
	case string:
		return []string{value}
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if name, ok := item.(string); ok {
				out = append(out, name)
			}
		}
		return out
	default:
		return nil
	}
}

func matchesAnyType(value any, types []string) bool {
	for _, want := range types {
		if matchesType(value, want) {
			return true
		}
	}
	return false
}

func matchesType(value any, want string) bool {
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	default:
		return false
	}
}

func containsLiteral(values []any, candidate any) bool {
	for _, value := range values {
		if literalEqual(value, candidate) {
			return true
		}
	}
	return false
}

func literalEqual(left, right any) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

// validateSchemaShape performs a bounded structural check of a declaration schema.
func validateSchemaShape(schema map[string]any, depth int) error {
	if depth > maxSchemaDepth {
		return NewError("TOOL_SIZE_LIMIT", 400)
	}
	if _, exists := schema["properties"]; exists {
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return NewError("UNSUPPORTED_TOOL_SCHEMA", 400)
		}
		for _, raw := range properties {
			child, ok := raw.(map[string]any)
			if !ok {
				return NewError("UNSUPPORTED_TOOL_SCHEMA", 400)
			}
			if err := validateSchemaShape(child, depth+1); err != nil {
				return err
			}
		}
	}
	if raw, exists := schema["items"]; exists {
		if items, ok := raw.(map[string]any); ok {
			if err := validateSchemaShape(items, depth+1); err != nil {
				return err
			}
		}
	}
	if required, exists := schema["required"]; exists {
		list, ok := required.([]any)
		if !ok {
			return NewError("UNSUPPORTED_TOOL_SCHEMA", 400)
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return NewError("UNSUPPORTED_TOOL_SCHEMA", 400)
			}
		}
	}
	return nil
}
