package geminiapp

// Minimal protobuf wire codec: no generated schema, no reflection.
// Mirrors native-wire/protobuf.mjs of the reference implementation, including
// its bounds and its refusal to silently resolve ambiguous singular fields.

const (
	maxMessageBytes = 16 << 20
	maxParseFields  = 100000
	maxParseDepth   = 32
	maxFieldNumber  = 0x1fffffff
)

// Wire types.
const (
	wireVarint     = 0
	wireFixed64    = 1
	wireBytes      = 2
	wireStartGroup = 3
	wireEndGroup   = 4
	wireFixed32    = 5
)

// WireField is one parsed protobuf field. Raw keeps the original bytes
// (unknown fields are preserved verbatim); Payload is the decoded value for
// length-delimited fields.
type WireField struct {
	Number  int
	Wire    int
	Raw     []byte
	Payload []byte
}

// AppendVarint appends an unsigned varint.
func AppendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func appendTag(dst []byte, number, wire int) []byte {
	return AppendVarint(dst, uint64(number)<<3|uint64(wire))
}

// AppendBytesField appends a length-delimited field.
func AppendBytesField(dst []byte, number int, value []byte) []byte {
	dst = appendTag(dst, number, wireBytes)
	dst = AppendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// AppendVarintField appends a varint field.
func AppendVarintField(dst []byte, number int, value uint64) []byte {
	dst = appendTag(dst, number, wireVarint)
	return AppendVarint(dst, value)
}

func readVarint(input []byte, start int) (uint64, int, error) {
	var value uint64
	for i := 0; i < 10; i++ {
		if start+i >= len(input) {
			return 0, 0, NewError("TRUNCATED_VARINT", 502)
		}
		b := input[start+i]
		if i == 9 && b > 1 {
			return 0, 0, NewError("VARINT_OVERFLOW", 502)
		}
		value |= uint64(b&0x7f) << (7 * uint(i))
		if b&0x80 == 0 {
			return value, start + i + 1, nil
		}
	}
	return 0, 0, NewError("INVALID_VARINT", 502)
}

// ParseFields parses a protobuf message into its fields. Unknown wire data is
// preserved rather than dropped; malformed input is rejected.
func ParseFields(input []byte) ([]WireField, error) {
	if len(input) > maxMessageBytes {
		return nil, NewError("MESSAGE_TOO_LARGE", 502)
	}
	count := 0
	fields, _, err := parseSequence(input, 0, -1, &count, 0)
	return fields, err
}

func parseSequence(input []byte, start, group int, count *int, depth int) ([]WireField, int, error) {
	if depth > maxParseDepth {
		return nil, 0, NewError("GROUP_DEPTH_EXCEEDED", 502)
	}
	fields := make([]WireField, 0, 8)
	pos := start
	for pos < len(input) {
		*count = *count + 1
		if *count > maxParseFields {
			return nil, 0, NewError("TOO_MANY_FIELDS", 502)
		}
		begin := pos
		key, next, err := readVarint(input, pos)
		if err != nil {
			return nil, 0, err
		}
		pos = next
		number := int(key >> 3)
		wire := int(key & 7)
		if number < 1 || number > maxFieldNumber {
			return nil, 0, NewError("INVALID_FIELD_NUMBER", 502)
		}
		if wire == wireEndGroup {
			if number != group {
				return nil, 0, NewError("UNMATCHED_END_GROUP", 502)
			}
			return fields, pos, nil
		}
		payloadStart := pos
		payloadEnd := pos
		switch wire {
		case wireVarint:
			_, next, err = readVarint(input, pos)
			if err != nil {
				return nil, 0, err
			}
			pos = next
			payloadEnd = pos
		case wireFixed64:
			pos += 8
			payloadEnd = pos
		case wireFixed32:
			pos += 4
			payloadEnd = pos
		case wireBytes:
			size, next, err := readVarint(input, pos)
			if err != nil {
				return nil, 0, err
			}
			if size > maxMessageBytes {
				return nil, 0, NewError("OVERSIZED_LENGTH", 502)
			}
			payloadStart = next
			pos = payloadStart + int(size)
			payloadEnd = pos
		case wireStartGroup:
			nested, end, err := parseSequence(input, pos, number, count, depth+1)
			_ = nested
			if err != nil {
				return nil, 0, err
			}
			payloadEnd = end
			pos = end
		default:
			return nil, 0, NewError("UNSUPPORTED_WIRE_TYPE", 502)
		}
		if pos > len(input) {
			return nil, 0, NewError("TRUNCATED_FIELD", 502)
		}
		fields = append(fields, WireField{
			Number:  number,
			Wire:    wire,
			Raw:     input[begin:pos],
			Payload: input[payloadStart:payloadEnd],
		})
	}
	if group != -1 {
		return nil, 0, NewError("UNTERMINATED_GROUP", 502)
	}
	return fields, pos, nil
}

// singularBytes returns the payload of a unique length-delimited field.
func singularBytes(fields []WireField, number int) ([]byte, bool, error) {
	var found []byte
	seen := false
	for _, field := range fields {
		if field.Number != number {
			continue
		}
		if seen {
			return nil, false, NewErrorf("DUPLICATE_SINGULAR_FIELD", 502, "field %d", number)
		}
		if field.Wire != wireBytes {
			return nil, false, NewErrorf("INVALID_FIELD_WIRE_TYPE", 502, "field %d", number)
		}
		found = field.Payload
		seen = true
	}
	return found, seen, nil
}

func uniqueField(fields []WireField, number int) (WireField, bool, error) {
	var out WireField
	seen := false
	for _, field := range fields {
		if field.Number != number {
			continue
		}
		if seen {
			return WireField{}, false, NewErrorf("DUPLICATE_SINGULAR_FIELD", 502, "field %d", number)
		}
		out = field
		seen = true
	}
	return out, seen, nil
}

func scalarValue(field WireField) (uint64, error) {
	if field.Wire != wireVarint {
		return 0, NewErrorf("INVALID_SCALAR_WIRE_TYPE", 502, "field %d", field.Number)
	}
	value, _, err := readVarint(field.Payload, 0)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// scalarField returns a varint field value and whether it was present.
func scalarField(fields []WireField, number int) (uint64, bool, error) {
	field, ok, err := uniqueField(fields, number)
	if err != nil || !ok {
		return 0, false, err
	}
	value, err := scalarValue(field)
	return value, true, err
}

// requireUniqueFields rejects messages where any listed field is ambiguous.
// Ambiguous upstream evidence must not be resolved by a first/last-field rule.
func requireUniqueFields(fields []WireField, numbers ...int) error {
	seen := make(map[int]struct{}, len(numbers))
	for _, field := range fields {
		for _, number := range numbers {
			if field.Number != number {
				continue
			}
			if _, duplicate := seen[number]; duplicate {
				return NewErrorf("INVALID_NATIVE_RESPONSE", 502, "duplicate field %d", number)
			}
			seen[number] = struct{}{}
		}
	}
	return nil
}
