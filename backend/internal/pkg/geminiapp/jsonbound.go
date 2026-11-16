package geminiapp

import (
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

// Bounded JSON parsing: rejects duplicate object keys, invalid UTF-8, non-finite
// numbers and trees that exceed the tool-protocol budget. Mirrors
// parseBoundedJson() in tool-protocol.mjs.

const (
	maxJSONBytes = 256 * 1024
	maxJSONNodes = 4096
	maxJSONDepth = 16
)

type jsonParser struct {
	text   string
	pos    int
	nodes  int
	status int
}

// ParseBoundedJSON parses text into map[string]any / []any / string / float64 /
// bool / nil. status is the HTTP status attached to protocol errors.
func ParseBoundedJSON(text string, status int) (any, error) {
	if status == 0 {
		status = 400
	}
	if len(text) > maxJSONBytes || !utf8.ValidString(text) {
		return nil, NewError("INVALID_TOOL_JSON", status)
	}
	p := &jsonParser{text: text, status: status}
	value, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.text) {
		return nil, p.bad()
	}
	return value, nil
}

func (p *jsonParser) bad() error {
	return NewError("INVALID_TOOL_JSON", p.status)
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) value(depth int) (any, error) {
	p.nodes++
	if p.nodes > maxJSONNodes || depth > maxJSONDepth {
		return nil, p.bad()
	}
	p.skipSpace()
	if p.pos >= len(p.text) {
		return nil, p.bad()
	}
	switch c := p.text[p.pos]; {
	case c == '"':
		return p.stringLiteral()
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == 't':
		return p.literal("true", true)
	case c == 'f':
		return p.literal("false", false)
	case c == 'n':
		return p.literal("null", nil)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return nil, p.bad()
	}
}

func (p *jsonParser) literal(word string, value any) (any, error) {
	if len(p.text)-p.pos < len(word) || p.text[p.pos:p.pos+len(word)] != word {
		return nil, p.bad()
	}
	p.pos += len(word)
	return value, nil
}

func (p *jsonParser) stringLiteral() (string, error) {
	start := p.pos
	p.pos++ // opening quote
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case '"':
			p.pos++
			var out string
			if err := json.Unmarshal([]byte(p.text[start:p.pos]), &out); err != nil {
				return "", p.bad()
			}
			if !utf8.ValidString(out) {
				return "", p.bad()
			}
			return out, nil
		case '\\':
			p.pos += 2
		default:
			p.pos++
		}
	}
	return "", p.bad()
}

func (p *jsonParser) number() (any, error) {
	start := p.pos
	if p.text[p.pos] == '-' {
		p.pos++
	}
	digits := func() int {
		count := 0
		for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
			p.pos++
			count++
		}
		return count
	}
	if digits() == 0 {
		return nil, p.bad()
	}
	if p.pos < len(p.text) && p.text[p.pos] == '.' {
		p.pos++
		if digits() == 0 {
			return nil, p.bad()
		}
	}
	if p.pos < len(p.text) && (p.text[p.pos] == 'e' || p.text[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			return nil, p.bad()
		}
	}
	literal := p.text[start:p.pos]
	value, err := strconv.ParseFloat(literal, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, p.bad()
	}
	return value, nil
}

func (p *jsonParser) object(depth int) (any, error) {
	p.pos++ // {
	result := map[string]any{}
	p.skipSpace()
	if p.pos < len(p.text) && p.text[p.pos] == '}' {
		p.pos++
		return result, nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.text) || p.text[p.pos] != '"' {
			return nil, p.bad()
		}
		key, err := p.stringLiteral()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, p.bad()
		}
		p.skipSpace()
		if p.pos >= len(p.text) || p.text[p.pos] != ':' {
			return nil, p.bad()
		}
		p.pos++
		child, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result[key] = child
		p.skipSpace()
		if p.pos >= len(p.text) {
			return nil, p.bad()
		}
		switch p.text[p.pos] {
		case '}':
			p.pos++
			return result, nil
		case ',':
			p.pos++
		default:
			return nil, p.bad()
		}
	}
}

func (p *jsonParser) array(depth int) (any, error) {
	p.pos++ // [
	result := []any{}
	p.skipSpace()
	if p.pos < len(p.text) && p.text[p.pos] == ']' {
		p.pos++
		return result, nil
	}
	for {
		child, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		result = append(result, child)
		p.skipSpace()
		if p.pos >= len(p.text) {
			return nil, p.bad()
		}
		switch p.text[p.pos] {
		case ']':
			p.pos++
			return result, nil
		case ',':
			p.pos++
		default:
			return nil, p.bad()
		}
	}
}
