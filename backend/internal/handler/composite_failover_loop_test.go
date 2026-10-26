//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// compositeFailoverRepo 每個平台各有一個帳號，用來驗證跨平台 failover 真的
// 換到另一個平台的帳號池（帳號層 failover 無法解釋這個結果）。
type compositeFailoverRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r compositeFailoverRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r compositeFailoverRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r compositeFailoverRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r compositeFailoverRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r compositeFailoverRepo) accountsForPlatform(platform string) []service.Account {
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out
}

// compositeFailoverUpstream 依帳號回應：第一個平台的帳號回 503，第二個平台的
// 帳號回 200，並記錄每次呼叫時請求體裡的 model。
type compositeFailoverUpstream struct {
	service.HTTPUpstream
	mu        sync.Mutex
	calls     []int64
	models    []string
	failForID map[int64]int
}

func (u *compositeFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	model := ""
	if req.Body != nil {
		if body, err := io.ReadAll(req.Body); err == nil {
			model = gjson.GetBytes(body, "model").String()
		}
	}
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	u.models = append(u.models, model)
	status := u.failForID[accountID]
	u.mu.Unlock()
	// 熱身探測（模型清單等）不是推論請求：若對它回錯誤，探測失敗會被當成
	// 帳號不可用而掩蓋跨平台 failover 本身的行為。
	if model == "" {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"data":[],"models":[]}`)),
		}, nil
	}
	if status != 0 {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"upstream unavailable"}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewBufferString(
			`{"id":"resp_ok","object":"response","model":"` + model + `","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}, nil
}

func (u *compositeFailoverUpstream) snapshot() ([]int64, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...), append([]string(nil), u.models...)
}

func newCompositeFailoverHandler(t *testing.T, upstream service.HTTPUpstream, accounts []service.Account) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	repo := compositeFailoverRepo{accounts: accounts}
	gatewayService := service.NewOpenAIGatewayService(
		repo,     // accountRepo
		nil,      // usageLogRepo
		nil,      // usageBillingRepo
		nil,      // userRepo
		nil,      // userSubRepo
		nil,      // userGroupRateRepo
		nil,      // cache
		cfg,      // cfg
		nil,      // schedulerSnapshot
		nil,      // concurrencyService
		nil,      // billingService
		nil,      // rateLimitService
		nil,      // billingCacheService
		upstream, // httpUpstream
		nil,      // deferredService
		nil,      // openAITokenProvider
		nil,      // grokTokenProvider
		nil,      // resolver
		nil,      // channelService
		nil,      // balanceNotifyService
		nil,      // settingService
		nil,      // userPlatformQuotaRepo
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(&concurrencyCacheMock{
			acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
			acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
		}),
		billingCache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, cfg,
	)
	handler.maxAccountSwitches = 1
	return handler
}

func newCompositeFailoverContext(t *testing.T, decision service.CompositeRouteDecision) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	groupID := int64(4242)
	body := []byte(`{"model":"deepseek-v4.1-flash","stream":false,"input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(service.WithCompositeRouteDecision(req.Context(), decision))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformComposite,
			AllowImageGeneration: true,
		},
		User: &service.User{ID: 100, Status: service.StatusActive},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 1})
	return c, rec
}

// compositeFailoverUpstreamModel returns an upstream model id that the target
// platform's account can actually serve, so the failover is exercised instead
// of being short-circuited by an eligibility filter.
func compositeFailoverUpstreamModel(platform string) string {
	if platform == service.PlatformDeepseek {
		return "deepseek-v4-flash"
	}
	return "upstream-" + platform
}

func compositeFailoverAccounts() []service.Account {
	return []service.Account{
		{
			ID: 1, Name: "opencode-go", Platform: service.PlatformOpenCodeGo,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Priority: 1, Credentials: map[string]any{"api_key": "opencode-key"},
		},
		{
			ID: 2, Name: "command-code", Platform: service.PlatformCommandCode,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Priority: 2, Credentials: map[string]any{"api_key": "command-code-key"},
		},
		{
			ID: 3, Name: "deepseek", Platform: service.PlatformDeepseek,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Priority: 3, Credentials: map[string]any{"api_key": "deepseek-key"},
		},
	}
}

func compositeFailoverDecision(platforms ...string) service.CompositeRouteDecision {
	candidates := make([]service.CompositeRouteCandidate, 0, len(platforms))
	for i, platform := range platforms {
		candidates = append(candidates, service.CompositeRouteCandidate{
			TargetPlatform: platform,
			UpstreamModel:  compositeFailoverUpstreamModel(platform),
			Priority:       (i + 1) * 10,
			RouteID:        int64(i + 1),
			Endpoint:       service.CompositeRouteEndpointResponses,
		})
	}
	return service.CompositeRouteDecision{
		Matched:        true,
		Source:         service.CompositeRouteSourceExplicit,
		GroupID:        4242,
		PublicModel:    "deepseek-v4.1-flash",
		TargetPlatform: candidates[0].TargetPlatform,
		UpstreamModel:  candidates[0].UpstreamModel,
		Endpoint:       service.CompositeRouteEndpointResponses,
		Candidates:     candidates,
	}
}

// 驗收情境：第一個平台的帳號回 503，第二個平台回 200 —— 同一個 client 請求
// 最終成功，且上游實際看到的是第二個平台的帳號與該平台的上游 model。
func TestCompositeResponsesFailsOverToNextPlatform(t *testing.T) {
	upstream := &compositeFailoverUpstream{failForID: map[int64]int{1: http.StatusServiceUnavailable}}
	handler := newCompositeFailoverHandler(t, upstream, compositeFailoverAccounts())
	decision := compositeFailoverDecision(service.PlatformOpenCodeGo, service.PlatformCommandCode, service.PlatformDeepseek)
	c, rec := newCompositeFailoverContext(t, decision)

	handler.Responses(c)

	calls, models := upstream.snapshot()
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	// 熱身探測（模型清單請求）不帶 model，會讓第二個平台被呼叫兩次。
	require.NotEmpty(t, calls)
	require.Equal(t, int64(1), calls[0], "the first attempt must use platform 1's account")
	require.Equal(t, int64(2), calls[len(calls)-1], "the successful attempt must use platform 2's account")
	require.Equal(t, "upstream-command_code", models[len(models)-1],
		"the successful attempt must forward the target platform's upstream model")
}

// 前兩個平台都失敗時走到第三個平台。
func TestCompositeResponsesWalksWholeChain(t *testing.T) {
	upstream := &compositeFailoverUpstream{failForID: map[int64]int{
		1: http.StatusServiceUnavailable,
		2: http.StatusServiceUnavailable,
	}}
	handler := newCompositeFailoverHandler(t, upstream, compositeFailoverAccounts())
	decision := compositeFailoverDecision(service.PlatformOpenCodeGo, service.PlatformCommandCode, service.PlatformDeepseek)
	c, rec := newCompositeFailoverContext(t, decision)

	handler.Responses(c)

	calls, _ := upstream.snapshot()
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, []int64{1, 2, 3}, calls)
}

// 全部平台都失敗時回錯誤且不無限迴圈：每個平台只嘗試一次。
func TestCompositeResponsesExhaustsChainWithoutLooping(t *testing.T) {
	upstream := &compositeFailoverUpstream{failForID: map[int64]int{
		1: http.StatusServiceUnavailable,
		2: http.StatusServiceUnavailable,
		3: http.StatusServiceUnavailable,
	}}
	handler := newCompositeFailoverHandler(t, upstream, compositeFailoverAccounts())
	decision := compositeFailoverDecision(service.PlatformOpenCodeGo, service.PlatformCommandCode, service.PlatformDeepseek)
	c, rec := newCompositeFailoverContext(t, decision)

	handler.Responses(c)

	calls, _ := upstream.snapshot()
	require.Equal(t, []int64{1, 2, 3}, calls, "each platform must be attempted exactly once")
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "error")
}

// 第一個平台沒有可用帳號（選號階段就失敗）也要換到第二個平台。
func TestCompositeResponsesFailsOverWhenFirstPlatformHasNoAccounts(t *testing.T) {
	accounts := compositeFailoverAccounts()
	// 拿掉 opencode_go 的帳號，模擬「停用第 1 順位帳號」。
	accounts = accounts[1:]
	upstream := &compositeFailoverUpstream{failForID: map[int64]int{}}
	handler := newCompositeFailoverHandler(t, upstream, accounts)
	decision := compositeFailoverDecision(service.PlatformOpenCodeGo, service.PlatformCommandCode, service.PlatformDeepseek)
	c, rec := newCompositeFailoverContext(t, decision)

	handler.Responses(c)

	calls, models := upstream.snapshot()
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.NotEmpty(t, calls)
	for _, accountID := range calls {
		require.Equal(t, int64(2), accountID, "no account of platform 1 exists, so none may be contacted")
	}
	require.Equal(t, "upstream-command_code", models[len(models)-1])
}

// 只有單一平台（沒有候選鏈）時行為與原本完全一致：不切換、直接回錯。
func TestCompositeResponsesSinglePlatformKeepsExistingBehavior(t *testing.T) {
	upstream := &compositeFailoverUpstream{failForID: map[int64]int{1: http.StatusServiceUnavailable}}
	handler := newCompositeFailoverHandler(t, upstream, compositeFailoverAccounts())
	decision := compositeFailoverDecision(service.PlatformOpenCodeGo)
	c, rec := newCompositeFailoverContext(t, decision)

	handler.Responses(c)

	calls, _ := upstream.snapshot()
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{1}, calls)
}
