package geminiapp

import (
	"encoding/binary"
	"encoding/json"
	"unicode/utf8"
)

// ImageAttachment is an uploaded image referenced by its opaque blob ID.
type ImageAttachment struct {
	BlobID   string
	MIMEType string
}

// GenerateRequest is one native ProcessQuery turn.
type GenerateRequest struct {
	UserText         string
	SystemText       string
	Images           []ImageAttachment
	ModeCategory     int
	ThinkingLevel    int
	TemporaryChat    bool
	ClientInstanceID string
	// Continuation is an opaque streaming continuation handle (field 6). Unused
	// by the product path, which always replays full history.
	Continuation []byte
}

// GenerateInfo is the decoded ClientGenerateResponse subset used by the gateway.
type GenerateInfo struct {
	Text       string
	HasText    bool
	ResponseID string

	ConversationID string
	RequestID      string

	Completed bool

	Fallback             *uint64
	Attribution          string
	DowngradeReason      *uint64
	ResolvedModeCategory *uint64
	ThinkingLevel        *uint64
	ModeVariantID        string
	RoutingVariantID     string

	// HasToolOperations reports native function-call operations (field 7). The
	// product path uses the prompt tool protocol and rejects these frames.
	HasToolOperations bool
}

const (
	fieldResponseModel   = 1
	fieldResponseIDs     = 2
	fieldToolOperations  = 7
	fieldCompleted       = 9
	fieldFallback        = 11
	fieldAttribution     = 30
	fieldDowngradeReason = 31
	fieldModeVariantID   = 39
	fieldRoutingVariant  = 40
	fieldResolvedMode    = 41
	fieldThinkingLevel   = 42
)

// BuildInitialRequest constructs the first gRPC message of a turn.
// It mirrors buildConsumerToolInitial() with the prompt tool protocol: no native
// function declarations are sent.
func BuildInitialRequest(req GenerateRequest) ([]byte, error) {
	if req.UserText == "" {
		return nil, NewError("INVALID_NATIVE_INPUT", 400)
	}
	if len(req.UserText) > maxMessageBytes {
		return nil, NewError("PROMPT_TOO_LARGE", 413)
	}
	if req.ModeCategory < 0 || req.ModeCategory > 255 {
		return nil, NewError("INVALID_MODE_CATEGORY", 400)
	}
	if req.ThinkingLevel < 0 || req.ThinkingLevel > ThinkingMax {
		return nil, NewError("INVALID_THINKING_LEVEL", 400)
	}
	if req.ThinkingLevel == ThinkingDeep {
		return nil, NewError("NATIVE_DEEP_THINK_UNSUPPORTED", 422)
	}
	if len(req.Images) > 4 {
		return nil, NewError("INVALID_NATIVE_IMAGES", 400)
	}

	instanceID := req.ClientInstanceID
	if instanceID == "" {
		return nil, NewError("CLIENT_INSTANCE_ID_REQUIRED", 400)
	}

	metadata := buildRequestMetadata(requestMetadataOptions{
		ClientType:                 21,
		LanguageCode:               "en",
		InterfaceLanguageCode:      "en",
		ClientVersion:              NativeCompatibilityVersion,
		ClientInstanceID:           instanceID,
		TriggerType:                1,
		UISource:                   1,
		ClientFunctionCapabilities: false,
	})

	clientGenerate := buildClientGenerate(clientGenerateOptions{
		UserText:      req.UserText,
		SystemText:    req.SystemText,
		Images:        req.Images,
		TemporaryChat: req.TemporaryChat,
	})

	// Fields 47 (mode) and 48 (thinking) are appended to the ClientGenerateRequest.
	template := make([]byte, 0, len(clientGenerate)+8)
	fields, err := ParseFields(clientGenerate)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if field.Number == 15 {
			// 15: "no tools" marker (1). Native tool declarations would set 0.
			template = AppendVarintField(template, 15, 1)
			continue
		}
		template = append(template, field.Raw...)
	}
	template = AppendVarintField(template, 47, uint64(req.ModeCategory))
	template = AppendVarintField(template, 48, uint64(req.ThinkingLevel))

	initial := make([]byte, 0, len(template)+64)
	initial = AppendBytesField(initial, 1, metadata) // ROBINRequestMetadata
	if len(req.Continuation) > 0 {
		initial = AppendBytesField(initial, 6, req.Continuation)
	}
	initial = AppendBytesField(initial, 1001, AppendBytesField(nil, 1, template))
	initial = AppendBytesField(initial, 1004, nil)

	out := AppendVarintField(nil, 1, 1) // turn sequence
	return AppendBytesField(out, 1000, initial), nil
}

type requestMetadataOptions struct {
	ClientType                 int
	LanguageCode               string
	InterfaceLanguageCode      string
	ClientVersion              string
	ClientInstanceID           string
	TriggerType                int
	UISource                   int
	ClientFunctionCapabilities bool
}

func buildRequestMetadata(opts requestMetadataOptions) []byte {
	var out []byte
	out = AppendBytesField(out, 1, AppendVarintField(nil, 1, uint64(opts.TriggerType)))
	if opts.InterfaceLanguageCode != "" {
		out = AppendBytesField(out, 2, AppendBytesField(nil, 1, []byte(opts.InterfaceLanguageCode)))
	}
	if opts.ClientVersion != "" {
		out = AppendBytesField(out, 3, AppendBytesField(nil, 1, []byte(opts.ClientVersion)))
	}
	out = AppendVarintField(out, 4, uint64(opts.ClientType))
	if opts.LanguageCode != "" {
		out = AppendBytesField(out, 7, []byte(opts.LanguageCode))
	}
	if opts.UISource != 0 {
		out = AppendVarintField(out, 5, uint64(opts.UISource))
	}
	if opts.ClientFunctionCapabilities {
		capabilities := AppendVarint(nil, 668)
		capabilities = AppendVarint(capabilities, 669)
		out = AppendBytesField(out, 6, AppendBytesField(nil, 2, capabilities))
	}
	if opts.ClientInstanceID != "" {
		out = AppendBytesField(out, 23, []byte(opts.ClientInstanceID))
	}
	return out
}

type clientGenerateOptions struct {
	UserText      string
	SystemText    string
	Images        []ImageAttachment
	TemporaryChat bool
}

// buildClientGenerate mirrors buildClientGenerate() in robin-current.mjs.
func buildClientGenerate(opts clientGenerateOptions) []byte {
	var out []byte

	// 1: UserQuery { text (1), attachments (3) }
	var query []byte
	query = AppendBytesField(query, 1, []byte(opts.UserText))
	for i, image := range opts.Images {
		blob := make([]byte, 0, 32)
		blob = AppendBytesField(blob, 1, []byte(image.BlobID))
		blob = AppendVarintField(blob, 2, 1)
		blob = AppendBytesField(blob, 3, []byte(image.MIMEType))
		blob = AppendVarintField(blob, 4, 1)
		attachment := AppendBytesField(nil, 1, blob)
		attachment = AppendBytesField(attachment, 2, []byte(imageName(i, image.MIMEType)))
		query = AppendBytesField(query, 3, attachment)
	}
	out = AppendBytesField(out, 1, query)

	out = AppendVarintField(out, 5, 1)
	out = AppendVarintField(out, 15, 1)
	if opts.SystemText != "" {
		out = AppendBytesField(out, 29, AppendBytesField(nil, 4, []byte(opts.SystemText)))
	}
	if opts.TemporaryChat {
		out = AppendVarintField(out, 31, 1)
	}
	out = AppendVarintField(out, 55, 0)
	out = AppendVarintField(out, 56, 0)
	return out
}

func imageName(index int, mimeType string) string {
	ext := "png"
	switch mimeType {
	case "image/jpeg":
		ext = "jpg"
	case "image/webp":
		ext = "webp"
	}
	return "image-" + itoa(index+1) + "." + ext
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[pos:])
}

// FrameGRPCMessage prefixes bytes with a 5-byte gRPC frame header.
func FrameGRPCMessage(payload []byte) ([]byte, error) {
	if len(payload) > maxMessageBytes {
		return nil, NewError("MESSAGE_TOO_LARGE", 413)
	}
	frame := make([]byte, 5+len(payload))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame, nil
}

// BuildWireFrames returns the complete gRPC request body for one turn.
func BuildWireFrames(req GenerateRequest) ([]byte, error) {
	initial, err := BuildInitialRequest(req)
	if err != nil {
		return nil, err
	}
	if len(initial) == 0 {
		return nil, NewError("INVALID_NATIVE_INPUT", 400)
	}

	text := AppendVarintField(nil, 1, 1)
	text = AppendBytesField(text, 1007, AppendBytesField(nil, 1, []byte(req.UserText)))

	end := AppendVarintField(nil, 1, 1)
	end = AppendBytesField(end, 1007, AppendBytesField(nil, 2, nil))

	var wire []byte
	for _, message := range [][]byte{initial, text, end} {
		frame, err := FrameGRPCMessage(message)
		if err != nil {
			return nil, err
		}
		wire = append(wire, frame...)
	}
	if len(wire) > maxMessageBytes {
		return nil, NewError("PROMPT_TOO_LARGE", 413)
	}
	return wire, nil
}

// GRPCFrameReader reassembles gRPC messages from streamed HTTP/2 DATA chunks.
type GRPCFrameReader struct {
	maxBytes uint32
	pending  []byte
	closed   bool
}

// NewGRPCFrameReader returns a reader bounded to maxBytes per message.
func NewGRPCFrameReader(maxBytes int) *GRPCFrameReader {
	if maxBytes <= 0 || maxBytes > maxMessageBytes {
		maxBytes = maxMessageBytes
	}
	return &GRPCFrameReader{maxBytes: uint32(maxBytes)}
}

// Push appends a DATA chunk and returns any complete messages.
func (r *GRPCFrameReader) Push(chunk []byte) ([][]byte, error) {
	if r.closed {
		return nil, NewError("READER_CLOSED", 500)
	}
	var frames [][]byte
	offset := 0
	for offset < len(chunk) {
		needed := 5 - len(r.pending)
		if len(r.pending) >= 5 {
			needed = 5 + int(binary.BigEndian.Uint32(r.pending[1:5])) - len(r.pending)
		}
		take := needed
		if remaining := len(chunk) - offset; remaining < take {
			take = remaining
		}
		r.pending = append(r.pending, chunk[offset:offset+take]...)
		offset += take
		if len(r.pending) < 5 {
			continue
		}
		if r.pending[0] != 0 {
			return nil, NewError("COMPRESSED_GRPC_UNSUPPORTED", 502)
		}
		length := binary.BigEndian.Uint32(r.pending[1:5])
		if length > r.maxBytes {
			return nil, NewError("GRPC_FRAME_EXCEEDS_BUDGET", 502)
		}
		if len(r.pending) == int(length)+5 {
			message := make([]byte, length)
			copy(message, r.pending[5:])
			frames = append(frames, message)
			r.pending = r.pending[:0]
		}
	}
	return frames, nil
}

// Finish reports truncated trailing data.
func (r *GRPCFrameReader) Finish() error {
	r.closed = true
	if len(r.pending) != 0 {
		return NewError("TRUNCATED_GRPC_STREAM", 502)
	}
	return nil
}

// DecodeFrame decodes one outer gRPC message into a GenerateInfo. Frames without
// the ClientGenerateResponse wrapper return (nil, nil).
func DecodeFrame(message []byte) (*GenerateInfo, error) {
	if len(message) > maxMessageBytes {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	outer, err := ParseFields(message)
	if err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	wrapper, ok, err := singularBytes(outer, 1003)
	if err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	if !ok {
		return nil, nil // opaque continuation handle
	}
	wrapperFields, err := ParseFields(wrapper)
	if err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	client, ok, err := singularBytes(wrapperFields, 1)
	if err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	if !ok {
		return nil, nil
	}
	return DecodeClientGenerateResponse(client)
}

// DecodeClientGenerateResponse decodes the inner ClientGenerateResponse.
func DecodeClientGenerateResponse(payload []byte) (*GenerateInfo, error) {
	fields, err := ParseFields(payload)
	if err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	}
	info := &GenerateInfo{}
	if err := requireUniqueFields(fields, fieldResponseModel, fieldCompleted, fieldFallback,
		fieldAttribution, fieldDowngradeReason, fieldResolvedMode, fieldThinkingLevel); err != nil {
		return nil, err
	}

	for _, field := range fields {
		if field.Number != fieldResponseModel {
			continue
		}
		if field.Wire != wireBytes {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		}
		modelFields, err := ParseFields(field.Payload)
		if err != nil {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		}
		if id, ok, err := singularBytes(modelFields, 1); err != nil {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		} else if ok {
			info.ResponseID = string(id)
		}
		if display, ok, err := singularBytes(modelFields, 2); err != nil {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		} else if ok {
			displayFields, err := ParseFields(display)
			if err != nil {
				return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
			}
			if text, ok, err := singularBytes(displayFields, 1); err != nil {
				return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
			} else if ok {
				if !utf8.Valid(text) {
					return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
				}
				info.Text = string(text)
				info.HasText = true
			}
		}
	}

	if ids, ok, err := singularBytes(fields, fieldResponseIDs); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		idFields, err := ParseFields(ids)
		if err != nil {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		}
		if conversation, ok, _ := singularBytes(idFields, 1); ok {
			info.ConversationID = string(conversation)
		}
		if request, ok, _ := singularBytes(idFields, 2); ok {
			info.RequestID = string(request)
		}
	}

	if value, ok, err := scalarField(fields, fieldCompleted); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		if value > 1 {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		}
		info.Completed = value == 1
	}
	if value, ok, err := scalarField(fields, fieldFallback); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		info.Fallback = &value
	}
	if value, ok, err := singularBytes(fields, fieldAttribution); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		if len(value) > 4096 || !utf8.Valid(value) {
			return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
		}
		info.Attribution = string(value)
	}
	if value, ok, err := scalarField(fields, fieldDowngradeReason); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		info.DowngradeReason = &value
	}
	if value, ok, err := scalarField(fields, fieldResolvedMode); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		info.ResolvedModeCategory = &value
	}
	if value, ok, err := scalarField(fields, fieldThinkingLevel); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		if value > 4 {
			return nil, NewError("INVALID_NATIVE_THINKING_LEVEL", 502)
		}
		info.ThinkingLevel = &value
	}
	if value, ok, err := singularBytes(fields, fieldModeVariantID); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		info.ModeVariantID = string(value)
	}
	if value, ok, err := singularBytes(fields, fieldRoutingVariant); err != nil {
		return nil, NewError("INVALID_NATIVE_RESPONSE", 502)
	} else if ok {
		info.RoutingVariantID = string(value)
	}
	for _, field := range fields {
		if field.Number == fieldToolOperations {
			info.HasToolOperations = true
			break
		}
	}
	return info, nil
}

// ErrorPayload renders an OpenAI-style error body for a protocol error.
func ErrorPayload(err error) []byte {
	code := "NATIVE_TURN_FAILED"
	message := "native request failed"
	if protocol, ok := AsError(err); ok {
		code = protocol.Code
		message = protocol.Error()
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "api_error",
			"code":    code,
			"message": message,
		},
	})
	return body
}
