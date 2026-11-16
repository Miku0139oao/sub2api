package geminiapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Native ProcessQuery transport. Mirrors gateway/native-transport.mjs: one
// bounded native turn per request, no retries, gRPC framing over HTTP/2.

const (
	maxResponseFrames  = 4096
	maxResponseFrame   = 1 << 20
	maxWireBytes       = 256 << 20
	defaultTurnTimeout = 180 * time.Second
)

// TurnOptions configures one native turn.
type TurnOptions struct {
	AccessToken      string
	Prompt           string
	ModeCategory     int
	ThinkingLevel    int
	TemporaryChat    bool
	ClientInstanceID string
	Timeout          time.Duration
	UserAgent        string
	// HTTPClient must permit HTTP/2 for NativeOrigin. nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// TurnStatus reports the transport outcome of a completed turn.
type TurnStatus struct {
	HTTPStatus int
	GRPCStatus *int
	Frames     int
	Received   int64
}

// ProcessQuery opens one native turn and invokes onFrame for every decoded gRPC
// message. It returns once the stream ends or an error occurs.
func ProcessQuery(ctx context.Context, opts TurnOptions, onFrame func(frame []byte) error) (*TurnStatus, error) {
	if opts.AccessToken == "" || len(opts.AccessToken) > 65536 {
		return nil, NewError("NATIVE_CREDENTIAL_REQUIRED", 503)
	}
	if opts.Prompt == "" {
		return nil, NewError("INVALID_NATIVE_INPUT", 400)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTurnTimeout
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	if opts.ClientInstanceID == "" {
		return nil, NewError("CLIENT_INSTANCE_ID_REQUIRED", 400)
	}

	wire, err := BuildWireFrames(GenerateRequest{
		UserText:         opts.Prompt,
		ModeCategory:     opts.ModeCategory,
		ThinkingLevel:    opts.ThinkingLevel,
		TemporaryChat:    opts.TemporaryChat,
		ClientInstanceID: opts.ClientInstanceID,
	})
	if err != nil {
		return nil, err
	}

	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(turnCtx, http.MethodPost, NativeOrigin+NativeRPC, bytes.NewReader(wire))
	if err != nil {
		return nil, NewError("NATIVE_CONNECTION_FAILED", 502)
	}
	request.ContentLength = int64(len(wire))
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = "sub2api-gemini-app/1.0"
	}
	request.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("Grpc-Timeout", strconv.FormatInt(timeout.Milliseconds(), 10)+"m")
	request.Header.Set("TE", "trailers")
	request.Header.Set("User-Agent", userAgent)

	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
			return nil, NewError("NATIVE_TRANSPORT_TIMEOUT", 504)
		}
		if errors.Is(err, context.Canceled) {
			return nil, NewError("REQUEST_CANCELLED", 499)
		}
		return nil, NewError("NATIVE_CONNECTION_FAILED", 502)
	}
	defer response.Body.Close()

	status := &TurnStatus{HTTPStatus: response.StatusCode}
	if err := mapHTTPStatus(response.StatusCode); err != nil {
		return status, err
	}

	reader := NewGRPCFrameReader(maxResponseFrame)
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			status.Received += int64(count)
			if status.Received > maxWireBytes {
				return status, NewError("NATIVE_RECEIVE_LIMIT", 502)
			}
			frames, err := reader.Push(buffer[:count])
			if err != nil {
				return status, err
			}
			for _, frame := range frames {
				status.Frames++
				if status.Frames > maxResponseFrames {
					return status, NewError("NATIVE_RESPONSE_LIMIT", 502)
				}
				if onFrame != nil {
					if err := onFrame(frame); err != nil {
						return status, err
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
				return status, NewError("NATIVE_TRANSPORT_TIMEOUT", 504)
			}
			if errors.Is(turnCtx.Err(), context.Canceled) {
				return status, NewError("REQUEST_CANCELLED", 499)
			}
			return status, NewError("NATIVE_STREAM_FAILED", 502)
		}
	}
	if err := reader.Finish(); err != nil {
		return status, err
	}
	if grpc := response.Trailer.Get("grpc-status"); grpc != "" {
		code, err := strconv.Atoi(grpc)
		if err != nil || code < 0 || code > 16 {
			return status, NewError("NATIVE_INVALID_STATUS", 502)
		}
		status.GRPCStatus = &code
		if err := mapGRPCStatus(code); err != nil {
			return status, err
		}
	}
	return status, nil
}

func mapHTTPStatus(code int) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusTooManyRequests:
		return NewError("NATIVE_QUOTA_EXHAUSTED", 429)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return NewError("NATIVE_PERMISSION_OR_AUTH", code)
	default:
		return NewError("NATIVE_HTTP_REJECTED", 502)
	}
}

func mapGRPCStatus(code int) error {
	switch code {
	case 0:
		return nil
	case 8:
		return NewError("NATIVE_QUOTA_EXHAUSTED", 429)
	case 7, 16:
		return NewError("NATIVE_PERMISSION_OR_AUTH", 403)
	case 9:
		return NewError("NATIVE_PRECONDITION_REQUIRED", 502)
	default:
		return NewError("NATIVE_TURN_FAILED", 502)
	}
}
