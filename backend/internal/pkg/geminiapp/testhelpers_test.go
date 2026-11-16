//go:build unit

package geminiapp

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// roundTripFunc adapts a function into an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// clientToTestServer returns a client that rewrites every request to serverURL.
// The pre-rewrite URL is appended to originalURLs when it is non-nil so tests can
// assert which fixed Google endpoint the product code selected.
func clientToTestServer(t *testing.T, serverURL string, originalURLs *[]string) *http.Client {
	t.Helper()
	target, err := url.Parse(serverURL)
	require.NoError(t, err)
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if originalURLs != nil {
			*originalURLs = append(*originalURLs, req.URL.String())
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

// requireProtocolError asserts err is a *Error with the given code and status.
func requireProtocolError(t *testing.T, err error, code string, status int) {
	t.Helper()
	require.Error(t, err)
	protocol, ok := AsError(err)
	require.True(t, ok, "expected *geminiapp.Error, got %T: %v", err, err)
	require.Equal(t, code, protocol.Code)
	require.Equal(t, status, protocol.Status)
}
