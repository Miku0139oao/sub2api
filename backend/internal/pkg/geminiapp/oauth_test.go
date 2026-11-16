//go:build unit

package geminiapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testGoogleClientID = "123456789012-abcdefghijklmnop.apps.googleusercontent.com"

func validOAuthProfile() *OAuthProfile {
	return &OAuthProfile{
		ClientID:     testGoogleClientID,
		ClientSecret: "test-secret",
		Scopes:       []string{GeminiAppAuthScope},
		RedirectURI:  "http://localhost:8085/oauth2callback",
	}
}

func TestOAuthProfileValidate(t *testing.T) {
	missingID := validOAuthProfile()
	missingID.ClientID = ""
	nonGoogle := validOAuthProfile()
	nonGoogle.ClientID = "my-client-id.apps.googleusercontent.com"
	upperCase := validOAuthProfile()
	upperCase.ClientID = "123456789012-ABCDEFGH.apps.googleusercontent.com"
	missingSecret := validOAuthProfile()
	missingSecret.ClientSecret = "   "
	noScopes := validOAuthProfile()
	noScopes.Scopes = nil
	badScope := validOAuthProfile()
	badScope.Scopes = []string{"https://example.com/not-google"}
	noRedirect := validOAuthProfile()
	noRedirect.RedirectURI = ""
	httpsRedirect := validOAuthProfile()
	httpsRedirect.RedirectURI = "https://localhost:8085/cb"
	remoteRedirect := validOAuthProfile()
	remoteRedirect.RedirectURI = "http://example.com/cb"
	unparsableRedirect := validOAuthProfile()
	unparsableRedirect.RedirectURI = "http://[::1]bad/cb"
	garbageRedirect := validOAuthProfile()
	garbageRedirect.RedirectURI = "://nope"

	tests := []struct {
		name       string
		profile    *OAuthProfile
		wantCode   string
		wantStatus int
	}{
		{name: "valid localhost", profile: validOAuthProfile()},
		{name: "nil profile", profile: nil, wantCode: "OAUTH_PROFILE_REQUIRED", wantStatus: 409},
		{name: "missing client id", profile: missingID, wantCode: "INVALID_OAUTH_CLIENT_ID", wantStatus: 400},
		{name: "non google client id", profile: nonGoogle, wantCode: "INVALID_OAUTH_CLIENT_ID", wantStatus: 400},
		{name: "uppercase client id", profile: upperCase, wantCode: "INVALID_OAUTH_CLIENT_ID", wantStatus: 400},
		{name: "missing client secret", profile: missingSecret, wantCode: "INVALID_OAUTH_CLIENT_SECRET", wantStatus: 400},
		{name: "no scopes", profile: noScopes, wantCode: "INVALID_OAUTH_PROFILE", wantStatus: 400},
		{name: "invalid scope", profile: badScope, wantCode: "INVALID_OAUTH_PROFILE", wantStatus: 400},
		{name: "missing redirect", profile: noRedirect, wantCode: "OAUTH_REDIRECT_REQUIRED", wantStatus: 400},
		{name: "https redirect", profile: httpsRedirect, wantCode: "OAUTH_REDIRECT_MUST_BE_LOOPBACK", wantStatus: 400},
		{name: "remote redirect", profile: remoteRedirect, wantCode: "OAUTH_REDIRECT_MUST_BE_LOOPBACK", wantStatus: 400},
		{name: "unparsable redirect", profile: unparsableRedirect, wantCode: "OAUTH_REDIRECT_MUST_BE_LOOPBACK", wantStatus: 400},
		{name: "garbage redirect", profile: garbageRedirect, wantCode: "OAUTH_REDIRECT_MUST_BE_LOOPBACK", wantStatus: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.profile.Validate()
			if test.wantCode == "" {
				require.NoError(t, err)
				return
			}
			requireProtocolError(t, err, test.wantCode, test.wantStatus)
		})
	}
}

func TestOAuthProfileValidateLoopbackVariants(t *testing.T) {
	for _, redirect := range []string{
		"http://localhost:8085/cb",
		"http://127.0.0.1:8085/cb",
		"http://127.0.0.1/cb",
	} {
		profile := validOAuthProfile()
		profile.RedirectURI = redirect
		require.NoError(t, profile.Validate(), "redirect %q", redirect)
	}
}

func TestBuildAuthorizationURL(t *testing.T) {
	profile := validOAuthProfile()
	profile.ForceConsent = true
	authorizationURL, err := BuildAuthorizationURL(profile, "state-123", "challenge-xyz")
	require.NoError(t, err)

	parsed, err := url.Parse(authorizationURL)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "accounts.google.com", parsed.Host)
	require.Equal(t, "/o/oauth2/auth", parsed.Path)

	query := parsed.Query()
	require.Equal(t, testGoogleClientID, query.Get("client_id"))
	require.Equal(t, profile.RedirectURI, query.Get("redirect_uri"))
	require.Equal(t, "code", query.Get("response_type"))
	require.Equal(t, GeminiAppAuthScope, query.Get("scope"))
	require.Equal(t, "offline", query.Get("access_type"))
	require.Equal(t, "state-123", query.Get("state"))
	require.Equal(t, "challenge-xyz", query.Get("code_challenge"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.Equal(t, "consent", query.Get("prompt"))
}

func TestBuildAuthorizationURLWithoutConsentAndWithAuthScopes(t *testing.T) {
	profile := validOAuthProfile()
	profile.AuthScopes = []string{"openid", "email"}

	authorizationURL, err := BuildAuthorizationURL(profile, "s", "c")
	require.NoError(t, err)
	parsed, err := url.Parse(authorizationURL)
	require.NoError(t, err)
	require.Equal(t, "openid email", parsed.Query().Get("scope"))
	require.Empty(t, parsed.Query().Get("prompt"))
}

func TestBuildAuthorizationURLRejectsInvalidProfile(t *testing.T) {
	profile := validOAuthProfile()
	profile.ClientID = "nope"
	_, err := BuildAuthorizationURL(profile, "s", "c")
	requireProtocolError(t, err, "INVALID_OAUTH_CLIENT_ID", 400)

	_, err = BuildAuthorizationURL(nil, "s", "c")
	requireProtocolError(t, err, "OAUTH_PROFILE_REQUIRED", 409)
}

// tokenTestServer responds with a fixed body and captures the request form.
func tokenTestServer(t *testing.T, status int, body string, form *url.Values, header *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header != nil {
			*header = r.Header.Clone()
		}
		if form != nil {
			require.NoError(t, r.ParseForm())
			*form = r.PostForm
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestExchangeCodeSuccess(t *testing.T) {
	var form url.Values
	var header http.Header
	var originalURLs []string
	server := tokenTestServer(t, http.StatusOK,
		`{"access_token":"ya29.token","refresh_token":"1//refresh","expires_in":3600,"scope":"https://www.googleapis.com/auth/flexible-api","expires_at":1700000000}`,
		&form, &header)
	defer server.Close()

	client := clientToTestServer(t, server.URL, &originalURLs)
	tokens, err := ExchangeCode(context.Background(), client, validOAuthProfile(), "auth-code", "verifier-123")
	require.NoError(t, err)
	require.Equal(t, "ya29.token", tokens.AccessToken)
	require.Equal(t, "1//refresh", tokens.RefreshToken)
	require.EqualValues(t, 3600, tokens.ExpiresIn)
	require.Equal(t, GeminiAppAuthScope, tokens.Scope)

	require.Equal(t, "authorization_code", form.Get("grant_type"))
	require.Equal(t, testGoogleClientID, form.Get("client_id"))
	require.Equal(t, "test-secret", form.Get("client_secret"))
	require.Equal(t, "auth-code", form.Get("code"))
	require.Equal(t, "verifier-123", form.Get("code_verifier"))
	require.Equal(t, "http://localhost:8085/oauth2callback", form.Get("redirect_uri"))
	require.Equal(t, "application/x-www-form-urlencoded", header.Get("Content-Type"))
	require.Len(t, originalURLs, 1)
	require.Equal(t, GoogleTokenURL, originalURLs[0])
}

func TestExchangeCodeExpiresInString(t *testing.T) {
	var originalURLs []string
	server := tokenTestServer(t, http.StatusOK, `{"access_token":"a","expires_in":"7200"}`, nil, nil)
	defer server.Close()

	tokens, err := ExchangeCode(context.Background(), clientToTestServer(t, server.URL, &originalURLs), validOAuthProfile(), "code", "verifier")
	require.NoError(t, err)
	require.EqualValues(t, 7200, tokens.ExpiresIn)
	require.Empty(t, tokens.RefreshToken)
	require.Empty(t, tokens.Scope)
}

func TestExchangeCodeMissingExpiresIn(t *testing.T) {
	server := tokenTestServer(t, http.StatusOK, `{"access_token":"a"}`, nil, nil)
	defer server.Close()

	tokens, err := ExchangeCode(context.Background(), clientToTestServer(t, server.URL, nil), validOAuthProfile(), "code", "verifier")
	require.NoError(t, err)
	require.Zero(t, tokens.ExpiresIn)
}

func TestExchangeCodeErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantCode   string
		wantStatus int
	}{
		{
			name: "invalid grant", status: http.StatusBadRequest,
			body:     `{"error":"invalid_grant","error_description":"bad code"}`,
			wantCode: "invalid_grant", wantStatus: 400,
		},
		{
			name: "invalid client", status: http.StatusUnauthorized,
			body:     `{"error":"invalid_client"}`,
			wantCode: "invalid_client", wantStatus: 400,
		},
		{
			name: "access denied", status: http.StatusBadRequest,
			body:     `{"error":"access_denied"}`,
			wantCode: "access_denied", wantStatus: 400,
		},
		{
			name: "unknown google error", status: http.StatusInternalServerError,
			body:     `{"error":"backend_error"}`,
			wantCode: "OAUTH_REJECTED", wantStatus: 400,
		},
		{
			name: "missing access token", status: http.StatusOK,
			body:     `{"refresh_token":"r"}`,
			wantCode: "OAUTH_REJECTED", wantStatus: 400,
		},
		{
			name: "non 2xx with access token", status: http.StatusBadGateway,
			body:     `{"access_token":"a"}`,
			wantCode: "OAUTH_REJECTED", wantStatus: 400,
		},
		{
			name: "malformed json", status: http.StatusOK,
			body:     `not json`,
			wantCode: "OAUTH_TOKEN_RESPONSE_INVALID", wantStatus: 502,
		},
		{
			name: "empty body", status: http.StatusOK,
			body:     ``,
			wantCode: "OAUTH_TOKEN_RESPONSE_INVALID", wantStatus: 502,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := tokenTestServer(t, test.status, test.body, nil, nil)
			defer server.Close()
			_, err := ExchangeCode(context.Background(), clientToTestServer(t, server.URL, nil), validOAuthProfile(), "code", "verifier")
			requireProtocolError(t, err, test.wantCode, test.wantStatus)
		})
	}
}

func TestExchangeCodeNetworkFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	_, err := ExchangeCode(context.Background(), client, validOAuthProfile(), "code", "verifier")
	requireProtocolError(t, err, "OAUTH_TOKEN_REQUEST_FAILED", 502)
}

func TestExchangeCodeValidatesProfileBeforeRequest(t *testing.T) {
	profile := validOAuthProfile()
	profile.ClientID = "invalid"
	_, err := ExchangeCode(context.Background(), nil, profile, "code", "verifier")
	requireProtocolError(t, err, "INVALID_OAUTH_CLIENT_ID", 400)
}

func TestRefreshAccessToken(t *testing.T) {
	var form url.Values
	var originalURLs []string
	server := tokenTestServer(t, http.StatusOK,
		`{"access_token":"new-token","expires_in":1800,"scope":"s"}`, &form, nil)
	defer server.Close()

	profile := validOAuthProfile()
	profile.Scopes = []string{"openid", "email"}
	profile.NativeLoopback = true
	tokens, err := RefreshAccessToken(context.Background(), clientToTestServer(t, server.URL, &originalURLs), profile, "1//stored")
	require.NoError(t, err)
	require.Equal(t, "new-token", tokens.AccessToken)
	require.EqualValues(t, 1800, tokens.ExpiresIn)

	require.Equal(t, "refresh_token", form.Get("grant_type"))
	require.Equal(t, testGoogleClientID, form.Get("client_id"))
	require.Equal(t, "test-secret", form.Get("client_secret"))
	require.Equal(t, "1//stored", form.Get("refresh_token"))
	require.Equal(t, "openid email", form.Get("scope"))
	require.Len(t, originalURLs, 1)
	require.Equal(t, GoogleNativeTokenURL, originalURLs[0])
}

func TestRefreshAccessTokenUsesStandardEndpoint(t *testing.T) {
	var originalURLs []string
	server := tokenTestServer(t, http.StatusOK, `{"access_token":"a"}`, nil, nil)
	defer server.Close()

	_, err := RefreshAccessToken(context.Background(), clientToTestServer(t, server.URL, &originalURLs), validOAuthProfile(), "r")
	require.NoError(t, err)
	require.Len(t, originalURLs, 1)
	require.Equal(t, GoogleTokenURL, originalURLs[0])
}

func TestRefreshAccessTokenRequiresRefreshToken(t *testing.T) {
	_, err := RefreshAccessToken(context.Background(), nil, validOAuthProfile(), "")
	requireProtocolError(t, err, "OAUTH_REFRESH_TOKEN_REQUIRED", 400)
	_, err = RefreshAccessToken(context.Background(), nil, validOAuthProfile(), "   ")
	requireProtocolError(t, err, "OAUTH_REFRESH_TOKEN_REQUIRED", 400)
}

func TestFetchIdentity(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		access     string
		wantCode   string
		wantStatus int
		wantEmail  string
		wantSub    string
	}{
		{
			name: "success", status: http.StatusOK,
			body:      `{"email":"user@example.com","email_verified":true,"sub":"123456789012345678901"}`,
			access:    "ya29.access",
			wantEmail: "user@example.com", wantSub: "123456789012345678901",
		},
		{
			name: "email not verified", status: http.StatusOK,
			body:   `{"email":"user@example.com","email_verified":false,"sub":"123"}`,
			access: "ya29.access", wantCode: "GOOGLE_VERIFIED_EMAIL_REQUIRED", wantStatus: 502,
		},
		{
			name: "email verification missing", status: http.StatusOK,
			body:   `{"email":"user@example.com","sub":"123"}`,
			access: "ya29.access", wantCode: "GOOGLE_VERIFIED_EMAIL_REQUIRED", wantStatus: 502,
		},
		{
			name: "invalid email", status: http.StatusOK,
			body:   `{"email":"not-an-email","email_verified":true,"sub":"123"}`,
			access: "ya29.access", wantCode: "GOOGLE_VERIFIED_EMAIL_REQUIRED", wantStatus: 502,
		},
		{
			name: "invalid subject", status: http.StatusOK,
			body:   `{"email":"user@example.com","email_verified":true,"sub":"bad subject"}`,
			access: "ya29.access", wantCode: "GOOGLE_VERIFIED_EMAIL_REQUIRED", wantStatus: 502,
		},
		{
			name: "non 200", status: http.StatusForbidden,
			body:   `{"email":"user@example.com","email_verified":true,"sub":"123"}`,
			access: "ya29.access", wantCode: "GOOGLE_IDENTITY_UNAVAILABLE", wantStatus: 502,
		},
		{
			name: "malformed json", status: http.StatusOK,
			body: `{`, access: "ya29.access",
			wantCode: "GOOGLE_IDENTITY_UNAVAILABLE", wantStatus: 502,
		},
		{
			name: "blank access token", status: http.StatusOK,
			body: `{}`, access: "   ",
			wantCode: "GOOGLE_IDENTITY_UNAVAILABLE", wantStatus: 502,
		},
		{
			name: "newline in access token", status: http.StatusOK,
			body: `{}`, access: "token\ninjected",
			wantCode: "GOOGLE_IDENTITY_UNAVAILABLE", wantStatus: 502,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var header http.Header
			server := tokenTestServer(t, test.status, test.body, nil, &header)
			defer server.Close()

			identity, err := FetchIdentity(context.Background(), clientToTestServer(t, server.URL, nil), test.access)
			if test.wantCode != "" {
				requireProtocolError(t, err, test.wantCode, test.wantStatus)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantEmail, identity.Email)
			require.Equal(t, test.wantSub, identity.Subject)
			require.Equal(t, "Bearer ya29.access", header.Get("Authorization"))
		})
	}
}

func TestFetchIdentityRejectsOversizedEmail(t *testing.T) {
	email := "a@" + strings.Repeat("b", 250) + ".com"
	server := tokenTestServer(t, http.StatusOK,
		`{"email":"`+email+`","email_verified":true,"sub":"123"}`, nil, nil)
	defer server.Close()

	_, err := FetchIdentity(context.Background(), clientToTestServer(t, server.URL, nil), "ya29.access")
	requireProtocolError(t, err, "GOOGLE_VERIFIED_EMAIL_REQUIRED", 502)
}

func TestOAuthProfileValidateRejectsInvalidAuthScopes(t *testing.T) {
	profile := &OAuthProfile{
		ClientID:     "123456789012-abc123def.apps.googleusercontent.com",
		ClientSecret: "secret",
		Scopes:       []string{"openid", "email"},
		AuthScopes:   []string{"https://www.googleapis.com/auth/flexible-api bad scope"},
		RedirectURI:  "http://localhost:18765",
	}

	err := profile.Validate()

	requireProtocolError(t, err, "INVALID_OAUTH_PROFILE", 400)
}
