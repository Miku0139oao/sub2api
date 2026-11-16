package geminiapp

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// Tool fence splitter: extracts `tool_call` fenced blocks from streamed model
// text while passing ordinary content through. Mirrors createToolCallSplitter()
// in tool-protocol.mjs.

type toolMode int

const (
	toolModeText toolMode = iota
	toolModeTool
	toolModeOrdinary
)

var (
	indentPattern    = regexp.MustCompile(`^(?: {4}|\t| {0,3}(?:>|[-+*][ \t]|\d+[.)][ \t]))`)
	fencePattern     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	toolLabelPattern = regexp.MustCompile(`^[ \t]*tool_call[ \t]*$`)
	blankLinePattern = regexp.MustCompile(`^[ \t]*$`)
)

// ToolEvent is either streamed content or a completed tool call.
type ToolEvent struct {
	Type string // "content" or "call"
	Text string
	Call *ToolCall
}

// ToolCallSplitter incrementally separates assistant text from tool calls.
type ToolCallSplitter struct {
	policy *ToolPolicy
	newID  func() string

	pending     string
	scanned     int
	seen        int
	closed      bool
	bytes       int
	mode        toolMode
	marker      byte
	fenceLength int
	toolBody    string
	toolBytes   int
	headerBytes int
	inlineTicks int
	ids         map[string]struct{}
}

// NewToolCallSplitter returns a splitter for the given policy.
func NewToolCallSplitter(policy *ToolPolicy) *ToolCallSplitter {
	return &ToolCallSplitter{
		policy: policy,
		newID:  newToolCallID,
		ids:    map[string]struct{}{},
		marker: '`',
	}
}

func newToolCallID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "call_" + strings.Repeat("0", 24)
	}
	return "call_" + hex.EncodeToString(buf)
}

// Push feeds text and returns any completed events.
func (s *ToolCallSplitter) Push(text string) ([]ToolEvent, error) {
	if s.closed {
		return nil, NewError("SPLITTER_CLOSED", 502)
	}
	s.bytes += len(text)
	if s.bytes > 8<<20 {
		return nil, NewError("RESPONSE_TOO_LARGE", 502)
	}
	s.pending += text
	return s.drain(false)
}

// Flush closes the stream and returns remaining events.
func (s *ToolCallSplitter) Flush() ([]ToolEvent, error) {
	if s.closed {
		return nil, NewError("SPLITTER_CLOSED", 502)
	}
	s.closed = true
	return s.drain(true)
}

// ToolCallCount returns how many calls were emitted.
func (s *ToolCallSplitter) ToolCallCount() int { return s.seen }

// Preview returns pending text that is safe to show before the stream ends.
func (s *ToolCallSplitter) Preview() string {
	if s.mode == toolModeTool {
		return ""
	}
	if s.mode == toolModeOrdinary {
		return s.pending
	}
	if strings.TrimSpace(s.pending) == "" && len(s.pending) <= 3 {
		return ""
	}
	tick := strings.IndexByte(s.pending, '`')
	if tick < 0 {
		return s.pending
	}
	if tick <= 3 && strings.TrimLeft(s.pending[:tick], " ") == "" {
		return ""
	}
	return s.pending[:tick]
}

func (s *ToolCallSplitter) drain(final bool) ([]ToolEvent, error) {
	events := make([]ToolEvent, 0, 2)
	consumed := 0
	from := s.scanned
	for {
		end := strings.IndexByte(s.pending[from:], '\n')
		if end < 0 {
			break
		}
		end += from
		if err := s.line(s.pending[consumed:end+1], &events); err != nil {
			return nil, err
		}
		consumed = end + 1
		from = consumed
	}
	if consumed > 0 {
		s.pending = s.pending[consumed:]
	}
	s.scanned = len(s.pending)
	if final && s.pending != "" {
		if err := s.line(s.pending, &events); err != nil {
			return nil, err
		}
		s.pending = ""
		s.scanned = 0
	}
	if s.mode == toolModeTool {
		if s.headerBytes+s.toolBytes+len(s.pending) > maxToolBytes {
			return nil, NewError("TOOL_SIZE_LIMIT", 502)
		}
		if final {
			return nil, NewError("TRUNCATED_TOOL_CALL", 502)
		}
	}
	return events, nil
}

func (s *ToolCallSplitter) line(raw string, events *[]ToolEvent) error {
	eol := ""
	text := raw
	if strings.HasSuffix(raw, "\r\n") {
		eol = "\r\n"
		text = raw[:len(raw)-2]
	} else if strings.HasSuffix(raw, "\n") {
		eol = "\n"
		text = raw[:len(raw)-1]
	}

	opening := fencePattern.FindStringSubmatch(text)
	run := ""
	info := ""
	if opening != nil {
		run = opening[1]
		info = text[len(opening[0]):]
	}

	if s.mode == toolModeTool {
		if run != "" && run[0] == '`' && len(run) >= s.fenceLength {
			if err := s.emitCall(events); err != nil {
				return err
			}
			s.mode = toolModeText
			s.toolBody = ""
			s.toolBytes = 0
			s.inlineTicks = 0
			if strings.TrimSpace(info) != "" {
				*events = append(*events, ToolEvent{Type: "content", Text: info + eol})
			} else if eol != "" {
				*events = append(*events, ToolEvent{Type: "content", Text: eol})
			}
			return nil
		}
		if run != "" && run[0] == '`' && strings.TrimSpace(info) != "" {
			return NewErrorf("MALFORMED_TOOL_CALL", 502, "closing_fence")
		}
		s.toolBody += raw
		s.toolBytes += len(raw)
		if s.toolBytes > maxToolBytes+4 {
			return NewError("TOOL_SIZE_LIMIT", 502)
		}
		return nil
	}

	if s.mode == toolModeOrdinary {
		*events = append(*events, ToolEvent{Type: "content", Text: raw})
		if run != "" && run[0] == s.marker && len(run) >= s.fenceLength && blankLinePattern.MatchString(info) {
			s.mode = toolModeText
		}
		return nil
	}

	if opening == nil || (run[0] == '`' && strings.Contains(info, "`")) {
		s.inlineText(text, raw, eol, events)
		return nil
	}
	label := strings.TrimSpace(info)
	if label == "tool_call" && run[0] == '`' {
		if eol == "" {
			return NewError("TRUNCATED_TOOL_CALL", 502)
		}
		s.mode = toolModeTool
		s.inlineTicks = 0
		s.fenceLength = len(run)
		s.headerBytes = len(raw)
		return nil
	}
	if strings.HasPrefix(label, "tool_call") {
		return NewErrorf("MALFORMED_TOOL_CALL", 502, "opening_fence")
	}
	if eol == "" && label != "" && strings.HasPrefix("tool_call", label) {
		return NewError("TRUNCATED_TOOL_CALL", 502)
	}
	s.mode = toolModeOrdinary
	s.inlineTicks = 0
	s.marker = run[0]
	s.fenceLength = len(run)
	*events = append(*events, ToolEvent{Type: "content", Text: raw})
	return nil
}

func (s *ToolCallSplitter) inlineText(text, raw, eol string, events *[]ToolEvent) {
	if indentPattern.MatchString(text) {
		s.inlineTicks = 0
		*events = append(*events, ToolEvent{Type: "content", Text: raw})
		return
	}
	if strings.TrimSpace(text) == "" {
		s.inlineTicks = 0
	}
	for i := 0; i < len(text); {
		if text[i] == '\\' && s.inlineTicks == 0 {
			i += 2
			for i < len(text) && i-1 < len(text) && text[i] == '`' && text[i-1] == '`' {
				i++
			}
			continue
		}
		if text[i] != '`' {
			i++
			continue
		}
		end := i
		for end < len(text) && text[end] == '`' {
			end++
		}
		length := end - i
		if s.inlineTicks == 0 && length >= 3 && eol != "" && toolLabelPattern.MatchString(text[end:]) {
			if i > 0 {
				*events = append(*events, ToolEvent{Type: "content", Text: text[:i]})
			}
			s.mode = toolModeTool
			s.fenceLength = length
			s.headerBytes = len(text[i:] + eol)
			return
		}
		if s.inlineTicks == 0 {
			s.inlineTicks = length
		} else if s.inlineTicks == length {
			s.inlineTicks = 0
		}
		i = end
	}
	*events = append(*events, ToolEvent{Type: "content", Text: raw})
}

func (s *ToolCallSplitter) emitCall(events *[]ToolEvent) error {
	parsed, err := ParseBoundedJSON(strings.TrimSpace(s.toolBody), 502)
	if err != nil {
		return err
	}
	call, ok := parsed.(map[string]any)
	if !ok {
		return NewError("INVALID_TOOL_CALL", 502)
	}
	if err := onlyKeys(call, "name", "arguments"); err != nil {
		return NewError("INVALID_TOOL_CALL", 502)
	}
	name, _ := call["name"].(string)
	arguments, _ := call["arguments"].(map[string]any)
	if !toolNamePattern.MatchString(name) || arguments == nil {
		return NewError("INVALID_TOOL_CALL", 502)
	}
	if err := ValidateToolCall(name, arguments, s.policy); err != nil {
		return err
	}
	s.seen++
	if s.seen > maxToolCalls {
		return NewError("TOOL_SIZE_LIMIT", 502)
	}
	id := s.newID()
	if _, duplicate := s.ids[id]; duplicate {
		return NewError("INVALID_TOOL_CALL_ID", 502)
	}
	s.ids[id] = struct{}{}
	*events = append(*events, ToolEvent{
		Type: "call",
		Call: &ToolCall{
			ID:       id,
			Type:     "function",
			Function: ToolFunction{Name: name, Arguments: mustMarshalCanonical(arguments)},
		},
	})
	return nil
}

func mustMarshalCanonical(value any) string {
	encoded, err := marshalJSON(value)
	if err != nil {
		return "{}"
	}
	return encoded
}
