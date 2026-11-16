//go:build unit

package geminiapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type turnCapture struct {
	path   string
	method string
	header http.Header
}

func requireTurnOpts(serverURL string) TurnOptions {
	return TurnOptions{
		AccessToken:      "ya29.token",
		Prompt:           "hello",
		ModeCategory:     ModeUltra,
		ThinkingLevel:    ThinkingExtended,
		ClientInstanceID: "instance-1",
		HTTPClient:       nil, // set by caller
	}
}

func TestProcessQuerySuccess(t *testing.T) {
	payloads := [][]byte{[]byte("frame-one"), []byte("frame-two")}
	var wire []byte
	for _, payload := range payloads {
		frame, err := FrameGRPCMessage(payload)
		require.NoError(t, err)
		wire = append(wire, frame...)
	}

	capture := make(chan turnCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture <- turnCapture{path: r.URL.Path, method: r.Method, header: r.Header.Clone()}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "grpc-status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)
		w.Header().Set("grpc-status", "0")
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)

	var got [][]byte
	status, err := ProcessQuery(context.Background(), opts, func(frame []byte) error {
		got = append(got, append([]byte(nil), frame...))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, payloads, got)
	require.Equal(t, http.StatusOK, status.HTTPStatus)
	require.Equal(t, len(payloads), status.Frames)
	require.Equal(t, int64(len(wire)), status.Received)
	require.NotNil(t, status.GRPCStatus)
	require.Equal(t, 0, *status.GRPCStatus)

	received := <-capture
	require.Equal(t, NativeRPC, received.path)
	require.Equal(t, http.MethodPost, received.method)
	require.Equal(t, "Bearer ya29.token", received.header.Get("Authorization"))
	require.Equal(t, "application/grpc", received.header.Get("Content-Type"))
	require.Equal(t, "trailers", received.header.Get("TE"))
	require.Equal(t, "sub2api-gemini-app/1.0", received.header.Get("User-Agent"))
	require.Equal(t, "180000m", received.header.Get("Grpc-Timeout"))
}

func TestProcessQueryCustomTimeoutAndUserAgent(t *testing.T) {
	capture := make(chan turnCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture <- turnCapture{path: r.URL.Path, method: r.Method, header: r.Header.Clone()}
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.Timeout = 5 * time.Second
	opts.UserAgent = "custom-agent/9"
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)

	_, err := ProcessQuery(context.Background(), opts, nil)
	require.NoError(t, err)

	received := <-capture
	require.Equal(t, "5000m", received.header.Get("Grpc-Timeout"))
	require.Equal(t, "custom-agent/9", received.header.Get("User-Agent"))
}

func TestProcessQueryInputValidation(t *testing.T) {
	longToken := strings.Repeat("a", 65537)
	tests := []struct {
		name   string
		opts   TurnOptions
		code   string
		status int
	}{
		{
			name: "missing access token", opts: TurnOptions{Prompt: "hi", ClientInstanceID: "id"},
			code: "NATIVE_CREDENTIAL_REQUIRED", status: 503,
		},
		{
			name: "oversized access token", opts: TurnOptions{AccessToken: longToken, Prompt: "hi", ClientInstanceID: "id"},
			code: "NATIVE_CREDENTIAL_REQUIRED", status: 503,
		},
		{
			name: "missing prompt", opts: TurnOptions{AccessToken: "token", ClientInstanceID: "id"},
			code: "INVALID_NATIVE_INPUT", status: 400,
		},
		{
			name: "missing instance id", opts: TurnOptions{AccessToken: "token", Prompt: "hi"},
			code: "CLIENT_INSTANCE_ID_REQUIRED", status: 400,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, err := ProcessQuery(context.Background(), test.opts, nil)
			require.Nil(t, status)
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestProcessQueryHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		code       string
		status     int
	}{
		{name: "too many requests", httpStatus: http.StatusTooManyRequests, code: "NATIVE_QUOTA_EXHAUSTED", status: 429},
		{name: "unauthorized", httpStatus: http.StatusUnauthorized, code: "NATIVE_PERMISSION_OR_AUTH", status: 401},
		{name: "forbidden", httpStatus: http.StatusForbidden, code: "NATIVE_PERMISSION_OR_AUTH", status: 403},
		{name: "internal error", httpStatus: http.StatusInternalServerError, code: "NATIVE_HTTP_REJECTED", status: 502},
		{name: "bad gateway", httpStatus: http.StatusBadGateway, code: "NATIVE_HTTP_REJECTED", status: 502},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.httpStatus)
			}))
			defer server.Close()

			opts := requireTurnOpts(server.URL)
			opts.HTTPClient = clientToTestServer(t, server.URL, nil)
			status, err := ProcessQuery(context.Background(), opts, nil)
			requireProtocolError(t, err, test.code, test.status)
			require.NotNil(t, status)
			require.Equal(t, test.httpStatus, status.HTTPStatus)
		})
	}
}

func TestProcessQueryRejectsMalformedFrames(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		code   string
		status int
	}{
		{
			name: "compressed frame", body: []byte{1, 0, 0, 0, 0},
			code: "COMPRESSED_GRPC_UNSUPPORTED", status: 502,
		},
		{
			// 0x00100001 is one byte over the 1 MiB per-message budget.
			name: "oversized frame", body: []byte{0, 0x00, 0x10, 0x00, 0x01},
			code: "GRPC_FRAME_EXCEEDS_BUDGET", status: 502,
		},
		{
			name: "truncated frame", body: []byte{0, 0, 0},
			code: "TRUNCATED_GRPC_STREAM", status: 502,
		},
		{
			name: "header without body", body: []byte{0, 0, 0, 0, 10},
			code: "TRUNCATED_GRPC_STREAM", status: 502,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/grpc")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()

			opts := requireTurnOpts(server.URL)
			opts.HTTPClient = clientToTestServer(t, server.URL, nil)
			_, err := ProcessQuery(context.Background(), opts, nil)
			requireProtocolError(t, err, test.code, test.status)
		})
	}
}

func TestProcessQueryContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ProcessQuery(ctx, opts, nil)
	requireProtocolError(t, err, "REQUEST_CANCELLED", 499)
}

func TestProcessQueryDeadlineBeforeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.Timeout = 20 * time.Millisecond
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)

	_, err := ProcessQuery(context.Background(), opts, nil)
	requireProtocolError(t, err, "NATIVE_TRANSPORT_TIMEOUT", 504)
}

func TestProcessQueryDeadlineDuringStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.Timeout = 30 * time.Millisecond
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)

	status, err := ProcessQuery(context.Background(), opts, nil)
	requireProtocolError(t, err, "NATIVE_TRANSPORT_TIMEOUT", 504)
	require.NotNil(t, status)
}

func TestProcessQueryFrameCallbackError(t *testing.T) {
	frame, err := FrameGRPCMessage([]byte("payload"))
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)
	sentinel := errors.New("frame callback failed")

	status, err := ProcessQuery(context.Background(), opts, func([]byte) error { return sentinel })
	require.ErrorIs(t, err, sentinel)
	require.NotNil(t, status)
	require.Equal(t, 1, status.Frames)
}

func TestProcessQueryResponseFrameLimit(t *testing.T) {
	emptyFrame, err := FrameGRPCMessage(nil)
	require.NoError(t, err)
	wire := make([]byte, 0, (maxResponseFrames+1)*len(emptyFrame))
	for i := 0; i <= maxResponseFrames; i++ {
		wire = append(wire, emptyFrame...)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)
	status, err := ProcessQuery(context.Background(), opts, nil)
	requireProtocolError(t, err, "NATIVE_RESPONSE_LIMIT", 502)
	require.NotNil(t, status)
	require.Equal(t, maxResponseFrames+1, status.Frames)
}

func TestProcessQueryTrailerMapping(t *testing.T) {
	frame, err := FrameGRPCMessage(nil)
	require.NoError(t, err)

	tests := []struct {
		name         string
		trailer      string
		code         string
		status       int
		wantGRPCCode bool
	}{
		{name: "ok", trailer: "0", wantGRPCCode: true},
		{name: "quota", trailer: "8", code: "NATIVE_QUOTA_EXHAUSTED", status: 429, wantGRPCCode: true},
		{name: "permission dropped", trailer: "7", code: "NATIVE_PERMISSION_OR_AUTH", status: 403, wantGRPCCode: true},
		{name: "unauthenticated", trailer: "16", code: "NATIVE_PERMISSION_OR_AUTH", status: 403, wantGRPCCode: true},
		{name: "precondition", trailer: "9", code: "NATIVE_PRECONDITION_REQUIRED", status: 502, wantGRPCCode: true},
		{name: "internal", trailer: "3", code: "NATIVE_TURN_FAILED", status: 502, wantGRPCCode: true},
		{name: "out of range", trailer: "99", code: "NATIVE_INVALID_STATUS", status: 502},
		{name: "non numeric", trailer: "x", code: "NATIVE_INVALID_STATUS", status: 502},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/grpc")
				w.Header().Set("Trailer", "grpc-status")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(frame)
				w.Header().Set("grpc-status", test.trailer)
			}))
			defer server.Close()

			opts := requireTurnOpts(server.URL)
			opts.HTTPClient = clientToTestServer(t, server.URL, nil)
			status, err := ProcessQuery(context.Background(), opts, nil)
			if test.code == "" {
				require.NoError(t, err)
				require.NotNil(t, status.GRPCStatus)
				require.Equal(t, 0, *status.GRPCStatus)
				return
			}
			requireProtocolError(t, err, test.code, test.status)
			require.NotNil(t, status)
			if test.wantGRPCCode {
				require.NotNil(t, status.GRPCStatus)
			} else {
				require.Nil(t, status.GRPCStatus)
			}
		})
	}
}

func TestProcessQueryWithoutTrailerLeavesGRPCStatusNil(t *testing.T) {
	frame, err := FrameGRPCMessage([]byte("payload"))
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	opts := requireTurnOpts(server.URL)
	opts.HTTPClient = clientToTestServer(t, server.URL, nil)
	status, err := ProcessQuery(context.Background(), opts, nil)
	require.NoError(t, err)
	require.Nil(t, status.GRPCStatus)
}
