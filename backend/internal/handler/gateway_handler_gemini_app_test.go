//go:build unit

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminiapp"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Coverage for the Gemini App branches of the gateway handler:
//   * GET /v1/models for a gemini_app group falls back to geminiapp.NativeModels.
//   * The dispatch branch in ChatCompletions/Responses routes gemini_app accounts
//     to geminiAppGatewayService and stamps service.GeminiAppUpstreamEndpoint.
//
// geminiAppGatewayService is a concrete *service.GeminiAppGatewayService, so it
// cannot be replaced by an interface stub. The configured path below uses a real
// service with a nil token provider: it writes its own 502 and returns an error,
// which lets the test observe the endpoint attribute without reaching Google.

type geminiAppDispatchRun struct {
	rec *httptest.ResponseRecorder
	c   *gin.Context
}

func newGeminiAppDispatchHandler(t *testing.T, group *service.Group, account *service.Account) *GatewayHandler {
	t.Helper()

	billingCache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil,
		&config.Config{}, nil)
	t.Cleanup(billingCache.Stop)

	snapshot := service.NewSchedulerSnapshotService(
		&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)

	return &GatewayHandler{
		gatewayService: service.NewGatewayService(
			nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil,
			&config.Config{},
			snapshot, nil, service.NewBillingService(&config.Config{}, nil), nil, nil, nil, nil,
			&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		),
		billingCacheService:      billingCache,
		cfg:                      &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}},
		maxAccountSwitches:       1,
		maxAccountSwitchesGemini: 1,
		concurrencyHelper: NewConcurrencyHelper(
			service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatNone, 0),
	}
}

func runGeminiAppDispatch(t *testing.T, endpoint string, withService bool) *geminiAppDispatchRun {
	t.Helper()
	gin.SetMode(gin.TestMode)

	groupID := int64(9601)
	const accountID int64 = 9602
	group := &service.Group{
		ID: groupID, Hydrated: true, Platform: service.PlatformGeminiApp,
		Status: service.StatusActive, RateMultiplier: 1,
	}
	account := &service.Account{
		ID: accountID, Platform: service.PlatformGeminiApp, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    "1800000000",
		},
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	h := newGeminiAppDispatchHandler(t, group, account)
	if withService {
		h.geminiAppGatewayService = service.NewGeminiAppGatewayService(nil, nil)
	}

	path, body := "/v1/chat/completions",
		`{"model":"gemini-argon","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	if endpoint == "responses" {
		path, body = "/v1/responses", `{"model":"gemini-argon","stream":false,"input":"hello"}`
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 9603, UserID: 9604, GroupID: &groupID, Group: group,
		User: &service.User{ID: 9604, Balance: 100},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 9604})

	if endpoint == "responses" {
		h.Responses(c)
	} else {
		h.ChatCompletions(c)
	}

	return &geminiAppDispatchRun{rec: rec, c: c}
}

func TestGatewayModels_GeminiAppGroupFallsBackToNativeModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID int64 = 9610

	// No schedulable gemini_app accounts: the handler must advertise the built-in
	// native aliases instead of the Claude defaults.
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		Group: &service.Group{ID: groupID, Platform: service.PlatformGeminiApp},
	})

	h.Models(c)

	require.Equal(t, http.StatusOK, rec.Code)
	var got gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "list", got.Object)
	require.Equal(t, []string{
		geminiapp.ModelArgon,
		geminiapp.ModelHydrogen,
		geminiapp.ModelFlashLite,
	}, modelIDsForTest(got.Data))
}

func TestGeminiAppDispatch_NilServiceReturnsBadGateway(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			run := runGeminiAppDispatch(t, endpoint, false)

			require.Equal(t, http.StatusBadGateway, run.rec.Code, run.rec.Body.String())
			var body struct {
				Error struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(run.rec.Body.Bytes(), &body))
			require.Contains(t, body.Error.Message, "Gemini App service is not configured")
			require.Equal(t, int64(9602), run.c.GetInt64(opsAccountIDKey),
				"the scheduled gemini_app account must be selected before dispatch")
		})
	}
}

func TestGeminiAppDispatch_UpstreamEndpointStampedBeforeServiceCall(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			run := runGeminiAppDispatch(t, endpoint, true)

			// The gateway service is real but has no token provider, so it writes a
			// 502 before contacting Google. The handler must have stamped the
			// upstream endpoint attribute before delegating.
			require.Equal(t, http.StatusBadGateway, run.rec.Code, run.rec.Body.String())
			require.Equal(t, service.GeminiAppUpstreamEndpoint,
				GetUpstreamEndpoint(run.c, service.PlatformGeminiApp))
			require.Contains(t, run.rec.Body.String(), "Gemini App is not configured")
		})
	}
}
