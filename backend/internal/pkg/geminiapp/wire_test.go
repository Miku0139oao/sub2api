//go:build unit

package geminiapp

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtobufRoundTrip(t *testing.T) {
	payload := AppendBytesField(nil, 1, []byte("hello"))
	payload = AppendVarintField(payload, 47, 8)
	payload = AppendBytesField(payload, 1001, AppendBytesField(nil, 1, []byte("nested")))

	fields, err := ParseFields(payload)
	require.NoError(t, err)
	require.Len(t, fields, 3)

	text, ok, err := singularBytes(fields, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "hello", string(text))

	mode, ok, err := scalarField(fields, 47)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 8, mode)

	nested, ok, err := singularBytes(fields, 1001)
	require.NoError(t, err)
	require.True(t, ok)
	nestedFields, err := ParseFields(nested)
	require.NoError(t, err)
	value, ok, err := singularBytes(nestedFields, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "nested", string(value))
}

func TestParseFieldsRejectsDuplicateSingular(t *testing.T) {
	payload := AppendBytesField(nil, 1, []byte("a"))
	payload = AppendBytesField(payload, 1, []byte("b"))
	fields, err := ParseFields(payload)
	require.NoError(t, err)
	_, _, err = singularBytes(fields, 1)
	require.Error(t, err)
}

func TestParseFieldsRejectsTruncated(t *testing.T) {
	payload := AppendBytesField(nil, 1, []byte("hello"))
	_, err := ParseFields(payload[:len(payload)-1])
	require.Error(t, err)
}

func TestBuildInitialRequestShape(t *testing.T) {
	initial, err := BuildInitialRequest(GenerateRequest{
		UserText:         "hi",
		ModeCategory:     ModeUltra,
		ThinkingLevel:    ThinkingExtended,
		ClientInstanceID: "00000000-0000-0000-0000-000000000000",
	})
	require.NoError(t, err)

	fields, err := ParseFields(initial)
	require.NoError(t, err)

	sequence, ok, err := scalarField(fields, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, sequence)

	inner, ok, err := singularBytes(fields, 1000)
	require.NoError(t, err)
	require.True(t, ok)
	innerFields, err := ParseFields(inner)
	require.NoError(t, err)
	require.Contains(t, fieldNumbers(innerFields), 1)    // metadata
	require.Contains(t, fieldNumbers(innerFields), 1001) // template
	require.Contains(t, fieldNumbers(innerFields), 1004) // empty marker

	template, ok, err := singularBytes(innerFields, 1001)
	require.NoError(t, err)
	require.True(t, ok)
	templateWrapper, err := ParseFields(template)
	require.NoError(t, err)
	generate, ok, err := singularBytes(templateWrapper, 1)
	require.NoError(t, err)
	require.True(t, ok)
	templateFields, err := ParseFields(generate)
	require.NoError(t, err)
	userQuery, ok, err := singularBytes(templateFields, 1)
	require.NoError(t, err)
	require.True(t, ok)
	queryMessage, err := ParseFields(userQuery)
	require.NoError(t, err)
	userText, ok, err := singularBytes(queryMessage, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "hi", string(userText))
	mode, ok, err := scalarField(templateFields, 47)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, ModeUltra, mode)
	thinking, ok, err := scalarField(templateFields, 48)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, ThinkingExtended, thinking)
}

func TestBuildInitialRequestRejectsDeepThink(t *testing.T) {
	_, err := BuildInitialRequest(GenerateRequest{
		UserText:         "hi",
		ModeCategory:     ModeUltra,
		ThinkingLevel:    ThinkingDeep,
		ClientInstanceID: "x",
	})
	require.Error(t, err)
	protocol, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, "NATIVE_DEEP_THINK_UNSUPPORTED", protocol.Code)
	require.Equal(t, 422, protocol.Status)
}

func TestBuildWireFrames(t *testing.T) {
	wire, err := BuildWireFrames(GenerateRequest{
		UserText:         "hello",
		ModeCategory:     ModeFast,
		ThinkingLevel:    ThinkingLow,
		ClientInstanceID: "00000000-0000-0000-0000-000000000000",
	})
	require.NoError(t, err)

	reader := NewGRPCFrameReader(maxMessageBytes)
	frames, err := reader.Push(wire)
	require.NoError(t, err)
	require.Len(t, frames, 3)
	require.NoError(t, reader.Finish())

	textFrame, err := ParseFields(frames[1])
	require.NoError(t, err)
	payload, ok, err := singularBytes(textFrame, 1007)
	require.NoError(t, err)
	require.True(t, ok)
	payloadFields, err := ParseFields(payload)
	require.NoError(t, err)
	text, ok, err := singularBytes(payloadFields, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "hello", string(text))
}

func TestGRPCFrameReaderHandlesSplitChunks(t *testing.T) {
	frame, err := FrameGRPCMessage([]byte("payload"))
	require.NoError(t, err)
	reader := NewGRPCFrameReader(maxMessageBytes)

	var frames [][]byte
	for _, chunk := range [][]byte{frame[:2], frame[2:6], frame[6:]} {
		got, err := reader.Push(chunk)
		require.NoError(t, err)
		frames = append(frames, got...)
	}
	require.Len(t, frames, 1)
	require.Equal(t, "payload", string(frames[0]))
	require.NoError(t, reader.Finish())
}

func TestGRPCFrameReaderRejectsTruncated(t *testing.T) {
	frame, err := FrameGRPCMessage([]byte("payload"))
	require.NoError(t, err)
	reader := NewGRPCFrameReader(maxMessageBytes)
	_, err = reader.Push(frame[:len(frame)-2])
	require.NoError(t, err)
	require.Error(t, reader.Finish())
}

func TestDecodeFrame(t *testing.T) {
	client := AppendBytesField(nil, 1, AppendBytesField(AppendBytesField(nil, 1, []byte("resp-1")), 2, AppendBytesField(nil, 1, []byte("hello world"))))
	client = AppendVarintField(client, 9, 1)
	client = AppendVarintField(client, 41, ModeUltra)
	client = AppendVarintField(client, 42, ThinkingExtended)
	client = AppendBytesField(client, 30, []byte("Gemini 4 Hydrogen"))

	outer := AppendBytesField(nil, 1003, AppendBytesField(nil, 1, client))
	info, err := DecodeFrame(outer)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "hello world", info.Text)
	require.True(t, info.HasText)
	require.Equal(t, "resp-1", info.ResponseID)
	require.True(t, info.Completed)
	require.EqualValues(t, ModeUltra, *info.ResolvedModeCategory)
	require.EqualValues(t, ThinkingExtended, *info.ThinkingLevel)
	require.Equal(t, "Gemini 4 Hydrogen", info.Attribution)
	require.False(t, info.HasToolOperations)
}

func TestDecodeFrameIgnoresOpaqueContinuation(t *testing.T) {
	info, err := DecodeFrame(AppendBytesField(nil, 1004, []byte("opaque")))
	require.NoError(t, err)
	require.Nil(t, info)
}

func TestDecodeFrameRejectsAmbiguousModel(t *testing.T) {
	model := AppendBytesField(nil, 1, AppendBytesField(AppendBytesField(nil, 1, []byte("a")), 2, AppendBytesField(nil, 1, []byte("x"))))
	client := bytes.Join([][]byte{
		AppendBytesField(nil, 1, model),
		AppendBytesField(nil, 1, model),
	}, nil)
	outer := AppendBytesField(nil, 1003, AppendBytesField(nil, 1, client))
	_, err := DecodeFrame(outer)
	require.Error(t, err)
}

func fieldNumbers(fields []WireField) []int {
	out := make([]int, 0, len(fields))
	for _, field := range fields {
		out = append(out, field.Number)
	}
	return out
}

func TestDecodeClientGenerateResponseDuplicateSingularFields(t *testing.T) {
	model := AppendBytesField(nil, 1, AppendBytesField(nil, 1, []byte("resp")))
	tests := []struct {
		name    string
		payload []byte
	}{
		{"model", append(cloneBytes(model), model...)},
		{"completed", append(AppendVarintField(nil, fieldCompleted, 1), AppendVarintField(nil, fieldCompleted, 1)...)},
		{"fallback", append(AppendVarintField(nil, fieldFallback, 1), AppendVarintField(nil, fieldFallback, 2)...)},
		{"attribution", append(AppendBytesField(nil, fieldAttribution, []byte("a")), AppendBytesField(nil, fieldAttribution, []byte("b"))...)},
		{"downgrade", append(AppendVarintField(nil, fieldDowngradeReason, 1), AppendVarintField(nil, fieldDowngradeReason, 2)...)},
		{"resolved mode", append(AppendVarintField(nil, fieldResolvedMode, 1), AppendVarintField(nil, fieldResolvedMode, 2)...)},
		{"thinking", append(AppendVarintField(nil, fieldThinkingLevel, 1), AppendVarintField(nil, fieldThinkingLevel, 1)...)},
		{"response ids", append(AppendBytesField(nil, fieldResponseIDs, nil), AppendBytesField(nil, fieldResponseIDs, nil)...)},
		{"mode variant id", append(AppendBytesField(nil, fieldModeVariantID, []byte("a")), AppendBytesField(nil, fieldModeVariantID, []byte("b"))...)},
		{"routing variant", append(AppendBytesField(nil, fieldRoutingVariant, []byte("a")), AppendBytesField(nil, fieldRoutingVariant, []byte("b"))...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, err := DecodeClientGenerateResponse(test.payload)
			require.Nil(t, info)
			requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
		})
	}
}

func TestDecodeClientGenerateResponseCompleted(t *testing.T) {
	trueInfo, err := DecodeClientGenerateResponse(AppendVarintField(nil, fieldCompleted, 1))
	require.NoError(t, err)
	require.True(t, trueInfo.Completed)

	falseInfo, err := DecodeClientGenerateResponse(AppendVarintField(nil, fieldCompleted, 0))
	require.NoError(t, err)
	require.False(t, falseInfo.Completed)

	missing, err := DecodeClientGenerateResponse(nil)
	require.NoError(t, err)
	require.False(t, missing.Completed)

	_, err = DecodeClientGenerateResponse(AppendVarintField(nil, fieldCompleted, 2))
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)

	// A length-delimited value on a scalar field is rejected.
	_, err = DecodeClientGenerateResponse(AppendBytesField(nil, fieldCompleted, []byte("x")))
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeClientGenerateResponseThinkingLevel(t *testing.T) {
	for _, level := range []uint64{0, 1, 2, 3, 4} {
		info, err := DecodeClientGenerateResponse(AppendVarintField(nil, fieldThinkingLevel, level))
		require.NoError(t, err, "level %d", level)
		require.NotNil(t, info.ThinkingLevel)
		require.EqualValues(t, level, *info.ThinkingLevel)
	}

	_, err := DecodeClientGenerateResponse(AppendVarintField(nil, fieldThinkingLevel, 5))
	requireProtocolError(t, err, "INVALID_NATIVE_THINKING_LEVEL", 502)
}

func TestDecodeClientGenerateResponseAttribution(t *testing.T) {
	info, err := DecodeClientGenerateResponse(AppendBytesField(nil, fieldAttribution, []byte("Gemini")))
	require.NoError(t, err)
	require.Equal(t, "Gemini", info.Attribution)

	exactLimit := bytes.Repeat([]byte("a"), 4096)
	info, err = DecodeClientGenerateResponse(AppendBytesField(nil, fieldAttribution, exactLimit))
	require.NoError(t, err)
	require.Len(t, info.Attribution, 4096)

	oversized := bytes.Repeat([]byte("a"), 4097)
	_, err = DecodeClientGenerateResponse(AppendBytesField(nil, fieldAttribution, oversized))
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)

	_, err = DecodeClientGenerateResponse(AppendBytesField(nil, fieldAttribution, []byte{0xff, 0xfe}))
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeClientGenerateResponseTextAndFlags(t *testing.T) {
	withText := AppendBytesField(nil, 1, AppendBytesField(nil, 2, AppendBytesField(nil, 1, []byte("hello"))))
	info, err := DecodeClientGenerateResponse(withText)
	require.NoError(t, err)
	require.True(t, info.HasText)
	require.Equal(t, "hello", info.Text)
	require.False(t, info.HasToolOperations)

	emptyText := AppendBytesField(nil, 1, AppendBytesField(nil, 2, AppendBytesField(nil, 1, nil)))
	info, err = DecodeClientGenerateResponse(emptyText)
	require.NoError(t, err)
	require.True(t, info.HasText)
	require.Empty(t, info.Text)

	withoutDisplay := AppendBytesField(nil, 1, AppendBytesField(nil, 1, []byte("resp-only")))
	info, err = DecodeClientGenerateResponse(withoutDisplay)
	require.NoError(t, err)
	require.False(t, info.HasText)
	require.Equal(t, "resp-only", info.ResponseID)

	withTools := AppendVarintField(nil, fieldToolOperations, 1)
	info, err = DecodeClientGenerateResponse(withTools)
	require.NoError(t, err)
	require.True(t, info.HasToolOperations)

	invalidUTF8 := AppendBytesField(nil, 1, AppendBytesField(nil, 2, AppendBytesField(nil, 1, []byte{0xff})))
	_, err = DecodeClientGenerateResponse(invalidUTF8)
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeClientGenerateResponseTextDuplicates(t *testing.T) {
	duplicateIDs := AppendBytesField(nil, 1,
		append(AppendBytesField(nil, 1, []byte("a")), AppendBytesField(nil, 1, []byte("b"))...))
	_, err := DecodeClientGenerateResponse(duplicateIDs)
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)

	duplicateText := AppendBytesField(nil, 1,
		AppendBytesField(nil, 2,
			append(AppendBytesField(nil, 1, []byte("a")), AppendBytesField(nil, 1, []byte("b"))...)))
	_, err = DecodeClientGenerateResponse(duplicateText)
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeClientGenerateResponseIDsAndVariants(t *testing.T) {
	ids := AppendBytesField(nil, 1, []byte("conversation-1"))
	ids = AppendBytesField(ids, 2, []byte("request-1"))
	payload := AppendBytesField(nil, fieldResponseIDs, ids)
	payload = AppendBytesField(payload, fieldModeVariantID, []byte("mode-1"))
	payload = AppendBytesField(payload, fieldRoutingVariant, []byte("route-1"))

	info, err := DecodeClientGenerateResponse(payload)
	require.NoError(t, err)
	require.Equal(t, "conversation-1", info.ConversationID)
	require.Equal(t, "request-1", info.RequestID)
	require.Equal(t, "mode-1", info.ModeVariantID)
	require.Equal(t, "route-1", info.RoutingVariantID)
}

func TestDecodeClientGenerateResponseScalarPointers(t *testing.T) {
	payload := AppendVarintField(nil, fieldFallback, 7)
	payload = AppendVarintField(payload, fieldDowngradeReason, 3)
	payload = AppendVarintField(payload, fieldResolvedMode, ModeFast)

	info, err := DecodeClientGenerateResponse(payload)
	require.NoError(t, err)
	require.NotNil(t, info.Fallback)
	require.EqualValues(t, 7, *info.Fallback)
	require.NotNil(t, info.DowngradeReason)
	require.EqualValues(t, 3, *info.DowngradeReason)
	require.NotNil(t, info.ResolvedModeCategory)
	require.EqualValues(t, ModeFast, *info.ResolvedModeCategory)
	require.Nil(t, info.ThinkingLevel)
}

func TestDecodeClientGenerateResponseIgnoresUnknownFields(t *testing.T) {
	payload := AppendBytesField(nil, 99, []byte("unknown"))
	payload = AppendVarintField(payload, 250, 1)
	info, err := DecodeClientGenerateResponse(payload)
	require.NoError(t, err)
	require.Equal(t, &GenerateInfo{}, info)
}

func TestDecodeClientGenerateResponseModelWrongWireType(t *testing.T) {
	_, err := DecodeClientGenerateResponse(AppendVarintField(nil, fieldResponseModel, 1))
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeFrameRejectsTruncatedPayload(t *testing.T) {
	outer := AppendBytesField(nil, 1003, AppendBytesField(nil, 1, AppendVarintField(nil, fieldCompleted, 1)))
	_, err := DecodeFrame(outer[:len(outer)-1])
	requireProtocolError(t, err, "INVALID_NATIVE_RESPONSE", 502)
}

func TestDecodeFrameWrapperWithoutClient(t *testing.T) {
	info, err := DecodeFrame(AppendBytesField(nil, 1003, AppendBytesField(nil, 2, []byte("nothing"))))
	require.NoError(t, err)
	require.Nil(t, info)

	info, err = DecodeFrame(nil)
	require.NoError(t, err)
	require.Nil(t, info)
}

func TestGRPCFrameReaderBoundaryHandling(t *testing.T) {
	first, err := FrameGRPCMessage([]byte("first"))
	require.NoError(t, err)
	second, err := FrameGRPCMessage([]byte("second"))
	require.NoError(t, err)
	empty, err := FrameGRPCMessage(nil)
	require.NoError(t, err)

	chunk := append(append(cloneBytes(first), second...), empty...)
	chunk = append(chunk, first[:3]...) // trailing partial header

	reader := NewGRPCFrameReader(maxMessageBytes)
	frames, err := reader.Push(chunk)
	require.NoError(t, err)
	require.Len(t, frames, 3)
	require.Equal(t, "first", string(frames[0]))
	require.Equal(t, "second", string(frames[1]))
	require.Empty(t, frames[2])
	require.Error(t, reader.Finish())
	_, err = reader.Push(nil)
	requireProtocolError(t, err, "READER_CLOSED", 500)
}

func TestGRPCFrameReaderHeaderOnlyTruncation(t *testing.T) {
	reader := NewGRPCFrameReader(maxMessageBytes)
	frames, err := reader.Push([]byte{0, 0, 0, 0, 8})
	require.NoError(t, err)
	require.Empty(t, frames)
	requireProtocolError(t, reader.Finish(), "TRUNCATED_GRPC_STREAM", 502)
}

func TestGRPCFrameReaderRejectsCompressedAndOversized(t *testing.T) {
	compressed := NewGRPCFrameReader(maxMessageBytes)
	_, err := compressed.Push([]byte{1, 0, 0, 0, 0})
	requireProtocolError(t, err, "COMPRESSED_GRPC_UNSUPPORTED", 502)

	oversized := NewGRPCFrameReader(8)
	_, err = oversized.Push([]byte{0, 0, 0, 0, 9})
	requireProtocolError(t, err, "GRPC_FRAME_EXCEEDS_BUDGET", 502)
}

func TestGRPCFrameReaderReturnsZeroLengthMessage(t *testing.T) {
	reader := NewGRPCFrameReader(maxMessageBytes)
	frames, err := reader.Push([]byte{0, 0, 0, 0, 0})
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.Empty(t, frames[0])
	require.NotNil(t, frames[0])
	require.NoError(t, reader.Finish())
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
